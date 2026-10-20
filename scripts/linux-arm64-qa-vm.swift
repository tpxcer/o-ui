import Foundation
import Virtualization
import Darwin

// 默认临时内存客体、两个只读光盘；可选专用测试盘，无网络适配器或共享目录。
final class ShutdownObserver: NSObject, VZVirtualMachineDelegate {
    func guestDidStop(_ machine: VZVirtualMachine) { print("PANEL_QA_GUEST_STOPPED"); exit(0) }
    func virtualMachine(_ machine: VZVirtualMachine, didStopWithError error: Error) {
        fputs("PANEL_QA_VM_FAILED: \(error)\n", stderr); exit(1)
    }
}

let arguments = Array(CommandLine.arguments.dropFirst())
guard arguments.count == 4 || (arguments.count == 6 && arguments[4] == "--disposable-test-disk") else {
    fatalError("用法：linux-arm64-qa-vm kernel initramfs alpine-iso qa-iso [--disposable-test-disk disposable-qa-data.raw]")
}
let inputs = arguments.prefix(4).map { URL(fileURLWithPath: $0) }
for input in inputs {
    let values = try input.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey])
    guard values.isRegularFile == true && values.isSymbolicLink != true else {
        fatalError("启动输入须为已核验普通文件")
    }
}
let configuration = VZVirtualMachineConfiguration()
configuration.cpuCount = 2
configuration.memorySize = 4 * 1024 * 1024 * 1024
let boot = VZLinuxBootLoader(kernelURL: inputs[0])
boot.initialRamdiskURL = inputs[1]
boot.commandLine = "console=hvc0 modules=loop,squashfs,virtio_blk"
configuration.bootLoader = boot
configuration.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: .standardInput, fileHandleForWriting: .standardOutput)
configuration.serialPorts = [serial]
configuration.storageDevices = try inputs.dropFirst(2).map { input in
    VZVirtioBlockDeviceConfiguration(attachment: try VZDiskImageStorageDeviceAttachment(
        url: input, readOnly: true, cachingMode: .cached, synchronizationMode: .fsync))
}
if arguments.count == 6 {
    let disk = URL(fileURLWithPath: arguments[5])
    let values = try disk.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey, .fileSizeKey])
    guard disk.lastPathComponent == "disposable-qa-data.raw", values.isRegularFile == true,
          values.isSymbolicLink != true, values.fileSize == 256 * 1024 * 1024 else {
        fatalError("只允许明确指定的256MiB专用临时测试盘，拒绝其它文件")
    }
    configuration.storageDevices.append(VZVirtioBlockDeviceConfiguration(attachment:
        try VZDiskImageStorageDeviceAttachment(url: disk, readOnly: false,
            cachingMode: .uncached, synchronizationMode: .fsync)))
}
try configuration.validate()
let observer = ShutdownObserver()
let machine = VZVirtualMachine(configuration: configuration)
machine.delegate = observer
// 只中断本runner拥有的客体；不向客体发送关机信号，用于明确的异常停止测试。
signal(SIGUSR1, SIG_IGN)
let forcedStop = DispatchSource.makeSignalSource(signal: SIGUSR1, queue: .main)
forcedStop.setEventHandler {
    machine.stop { error in
        if let error = error { fputs("PANEL_QA_FORCED_STOP_FAILED: \(error)\n", stderr); exit(1) }
        print("PANEL_QA_FORCED_STOP_NO_GUEST_SHUTDOWN")
        exit(0)
    }
}
forcedStop.resume()
machine.start { result in
    switch result {
    case .success: print(arguments.count == 4 ? "PANEL_QA_VM_STARTED_NO_NETWORK_READONLY_MEDIA" : "PANEL_QA_VM_STARTED_NO_NETWORK_DISPOSABLE_DATA_DISK")
    case .failure(let error): fputs("PANEL_QA_VM_FAILED: \(error)\n", stderr); exit(1)
    }
}
RunLoop.main.run()
