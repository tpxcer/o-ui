package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"independent-panel/internal/core"
	"independent-panel/internal/engine"
	"independent-panel/internal/i18n"
	"independent-panel/internal/localapi"
	"independent-panel/internal/store"
)

// 面板设置中的“重启面板”：run正常返回（defer已停核心、关库、释放锁）后原地重新执行自身。
var restartRequested bool

func main() {
	if code, handled := engine.Supervisor(); handled {
		os.Exit(code)
	}
	code := run()
	if code == 0 && restartRequested {
		exe, err := os.Executable()
		if err == nil {
			err = syscall.Exec(exe, os.Args, os.Environ())
		}
		log.Print(i18n.Pick(i18n.EnvLang(), "面板重启失败：", "Panel restart failed: "), err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run() int {
	// 命令行按LC_ALL/LC_MESSAGES/LANG选择简体中文或英文；内部错误按已登记文案翻译。
	lang := i18n.EnvLang()
	say := func(zh, en string) string { return i18n.Pick(lang, zh, en) }
	text := func(err error) string { return i18n.Translate(err.Error(), lang) }
	listen := flag.String("listen", "127.0.0.1:5180", say("无已保存访问设置时的本机初始监听，127.0.0.1非特权端口", "Initial local listen address when no access settings are saved (127.0.0.1, unprivileged port)"))
	data := flag.String("data", ".runtime/core", say("核心数据目录", "Core data directory"))
	stateDir := flag.String("state", ".runtime/panel", say("o-ui 数据库目录，不导入旧数据", "o-ui database directory (old data is never imported)"))
	dist := flag.String("dist", "dist", say("前端构建目录", "Frontend build directory"))
	recoverAccess := flag.Bool("recover-panel-access", false, say("停面板后离线恢复本机HTTP访问，不重置账号或业务，不启动服务", "Offline restore of local HTTP access after stopping the panel; does not reset accounts or data and starts nothing"))
	confirmation := flag.String("confirm-recovery", "", say("离线恢复须精确填写：恢复面板本机访问", "Offline recovery requires exactly: 恢复面板本机访问"))
	initAdmin := flag.Bool("init-panel-admin", false, say("安装器离线设置管理员（密码6–72字节，从标准输入读一行），面板须已停止，不启动服务", "Installer: set the administrator offline (password of 6–72 bytes read as one line from stdin); the panel must be stopped; starts nothing"))
	username := flag.String("username", "", say("与-init-panel-admin或-reset-panel-admin一起使用的管理员用户名", "Administrator username for -init-panel-admin or -reset-panel-admin"))
	enableHTTPS := flag.Bool("enable-panel-https", false, say("安装器离线写入面板HTTPS证书与固定访问来源，面板须已停止，下次启动生效", "Installer: write the panel HTTPS certificate and fixed origin offline; the panel must be stopped; applies on next start"))
	panelCert := flag.String("panel-cert", "", say("面板证书（含链）PEM的绝对路径", "Absolute path of the panel certificate PEM (with chain)"))
	panelKey := flag.String("panel-key", "", say("面板私钥PEM的绝对路径", "Absolute path of the panel private key PEM"))
	panelListen := flag.String("panel-listen", "", say("HTTPS直接监听，如0.0.0.0:23456", "HTTPS listen address, e.g. 0.0.0.0:23456"))
	panelOrigin := flag.String("panel-origin", "", say("固定HTTPS访问来源，如https://panel.example.com:23456", "Fixed HTTPS origin, e.g. https://panel.example.com:23456"))
	confirmHTTPS := flag.String("confirm-https", "", say("须精确填写：确认启用HTTPS非本机访问", "Requires exactly: 确认启用HTTPS非本机访问"))
	installCore := flag.String("install-core", "", say("安装器离线下载、校验并选中官方Xray：latest为最新正式版（不含预发布），或填精确版本；须用-data指定核心目录，面板须已停止，不启动代理", "Installer: download, verify and select official Xray offline: latest is the newest stable release (no prereleases), or an exact version; requires -data with the core directory; the panel must be stopped; starts no proxy"))
	setPath := flag.String("set-panel-path", "", say("管理命令离线设置访问路径（/或/段/），须用-state指定状态目录，面板须已停止，下次启动生效", "Management: set the access path offline (/ or /segment/); requires -state; the panel must be stopped; applies on next start"))
	resetAdmin := flag.Bool("reset-panel-admin", false, say("管理命令离线重置用户名和密码（密码6–72字节，从标准输入读一行），面板须已停止，全部登录失效", "Management: reset username and password offline (password of 6–72 bytes read as one line from stdin); the panel must be stopped; all sessions are signed out"))
	showPanel := flag.Bool("show-panel", false, say("管理命令以JSON输出当前面板设置（不含密码），面板运行中也可用", "Management: print the current panel settings as JSON (no password); works while the panel runs"))
	flag.Parse()
	host, port, err := net.SplitHostPort(*listen)
	if err != nil || host != "127.0.0.1" {
		log.Fatal(say("初始监听必须绑定127.0.0.1；非本机HTTPS须初始化后在面板设置中显式启用", "Initial listen must bind 127.0.0.1; remote HTTPS must be enabled in Panel settings after setup"))
	}
	portNumber, err := net.LookupPort("tcp", port)
	if err != nil || portNumber < 1024 {
		log.Fatal(say("仅允许非特权端口", "Only unprivileged ports are allowed"))
	}
	explicitState, explicitData := false, false
	flag.Visit(func(f *flag.Flag) {
		explicitState = explicitState || f.Name == "state"
		explicitData = explicitData || f.Name == "data"
	})
	modes := 0
	for _, on := range []bool{*recoverAccess, *initAdmin, *enableHTTPS, *installCore != "", *setPath != "", *resetAdmin, *showPanel} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		log.Print(say("离线命令一次只能执行一个", "Run one offline command at a time"))
		return 1
	}
	if *installCore != "" {
		if !explicitData {
			log.Print(say("离线安装核心必须用-data明确指定核心目录的绝对路径", "Offline core installation requires -data with the absolute path of the core directory"))
			return 1
		}
		// 下载与校验较慢，给足时间；失败不影响已有核心与选择。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		manager, err := core.NewManager(*data)
		if err != nil {
			log.Print(text(err))
			return 1
		}
		defer manager.Close()
		version := *installCore
		if version == "latest" {
			releases, err := manager.Releases(ctx)
			if err == nil {
				version, err = core.LatestStable(releases)
			}
			if err != nil {
				log.Print(text(err))
				return 1
			}
		}
		if err := manager.Install(ctx, version); err != nil {
			log.Print(text(err))
			return 1
		}
		fmt.Printf(say("已安装并选中Xray %s（官方包已校验）；未启动代理。\n", "Xray %s installed and selected (official package verified); no proxy was started.\n"), manager.Selected())
		return 0
	}
	if *initAdmin || *enableHTTPS || *setPath != "" || *resetAdmin || *showPanel {
		if !explicitState {
			log.Print(say("离线设置必须用-state明确指定状态目录的绝对路径", "Offline setup requires -state with the absolute path of the state directory"))
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if *initAdmin || *resetAdmin {
			// 密码只从标准输入读取，避免出现在进程参数中。
			line, err := bufio.NewReader(io.LimitReader(os.Stdin, 128)).ReadString('\n')
			password := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if err == nil || err == io.EOF {
				if *initAdmin {
					err = localapi.InitPanelAdmin(ctx, *stateDir, *username, password)
				} else {
					err = localapi.ResetPanelAdmin(ctx, *stateDir, *username, password)
				}
			}
			if err != nil {
				log.Print(text(err))
				return 1
			}
			if *initAdmin {
				fmt.Println(say("管理员已离线设置；尚未启动服务。", "Administrator set offline; nothing was started."))
			} else {
				fmt.Println(say("用户名和密码已离线重置，全部登录已失效；尚未启动服务。", "Username and password reset offline and all sessions signed out; nothing was started."))
			}
			return 0
		}
		if *setPath != "" {
			if err := localapi.SetPanelPath(ctx, *stateDir, *setPath); err != nil {
				log.Print(text(err))
				return 1
			}
			fmt.Printf(say("访问路径已设为%s；下次启动生效，尚未启动服务。\n", "Access path set to %s; applies on next start, nothing was started.\n"), *setPath)
			return 0
		}
		if *showPanel {
			info, err := localapi.ReadPanelInfo(ctx, *stateDir)
			if err != nil {
				log.Print(text(err))
				return 1
			}
			data, _ := json.Marshal(info)
			fmt.Println(string(data))
			return 0
		}
		if err := localapi.EnablePanelHTTPS(ctx, *stateDir, *panelCert, *panelKey, *panelListen, *panelOrigin, *confirmHTTPS); err != nil {
			log.Print(text(err))
			return 1
		}
		fmt.Printf(say("已写入面板HTTPS设置：%s/；下次启动生效，尚未启动服务。\n", "Panel HTTPS settings saved: %s/; applies on next start, nothing was started.\n"), *panelOrigin)
		return 0
	}
	if *username != "" || *panelCert != "" || *panelKey != "" || *panelListen != "" || *panelOrigin != "" || *confirmHTTPS != "" {
		log.Print(say("这些参数只与对应的离线设置命令一起使用，不启动服务", "These options are only used with their offline setup command and start nothing"))
		return 1
	}
	if *recoverAccess {
		if !explicitState {
			log.Print(say("离线恢复必须用-state明确指定现有状态目录的绝对路径", "Offline recovery requires -state with the absolute path of an existing state directory"))
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		backup, err := localapi.RecoverPanelAccess(ctx, *stateDir, *listen, *confirmation)
		if backup != "" {
			fmt.Printf(say("原访问/证书路径设置备份：%s\n", "Backup of the previous access/certificate path settings: %s\n"), backup)
		}
		if err != nil {
			log.Print(text(err))
			return 1
		}
		fmt.Printf(say("已恢复本机HTTP访问设置：http://%s/；面板HTTPS已停用，账号/业务/节点/核心未重置。尚未启动服务，请手动正常启动后登录。\n", "Local HTTP access restored: http://%s/; panel HTTPS is disabled and accounts/data/nodes/cores are unchanged. Nothing was started; start the panel normally and sign in.\n"), *listen)
		return 0
	}
	if *confirmation != "" {
		log.Print(say("confirm-recovery仅与recover-panel-access一起使用，不启动服务", "confirm-recovery is only used with recover-panel-access and starts nothing"))
		return 1
	}
	root, err := filepath.Abs(*data)
	if err != nil {
		log.Fatal(err)
	}
	manager, err := core.NewManager(root)
	if err != nil {
		log.Fatal(text(err))
	}
	defer manager.Close()
	state, err := store.Open(*stateDir)
	if err != nil {
		log.Fatal(text(err))
	}
	defer state.Close()
	panelTLS, err := localapi.LoadPanelTLS(context.Background(), state)
	if err != nil {
		log.Fatal(text(err))
	}
	access, err := localapi.LoadPanelAccess(context.Background(), state, *listen, panelTLS)
	if err != nil {
		log.Print(text(err))
		return 1
	}
	runtime, err := localapi.NewRuntime(manager, state, filepath.Join(*stateDir, "runtime"), panelTLS)
	if err != nil {
		log.Fatal(text(err))
	}
	defer runtime.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go runtime.RunTelegram(ctx)
	go runtime.RunIPLimit(ctx)
	go func() {
		if err := runtime.RestorePortHopping(ctx); err != nil {
			log.Print(say("恢复HY2端口跳跃转发失败：", "Failed to restore HY2 port hopping: "), text(err))
		}
	}()
	go func() {
		if started, err := runtime.ResumeOnStart(ctx); err != nil {
			log.Print(say("自动恢复上次应用失败，核心保持停止：", "Auto-resume of the last applied configuration failed; the core stays stopped: "), text(err))
		} else if started {
			fmt.Println(say("已按上次退出时运行的已应用配置自动恢复核心", "Core auto-resumed with the configuration that was running at last exit"))
		}
	}()
	server := &http.Server{Addr: access.Data.Listen,
		Handler:           localapi.PanelHandler(manager, state, *dist, access, runtime),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 4 * time.Minute, IdleTimeout: time.Minute}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
		case <-localapi.RestartRequests():
			restartRequested = true
			fmt.Println(say("收到重启请求，正在正常关闭后重新启动面板", "Restart requested; shutting down cleanly and starting the panel again"))
			stop()
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	scheme := "http"
	if panelTLS != nil {
		scheme = "https"
		server.TLSConfig = panelTLS.Config()
	}
	if access.Data.Public {
		fmt.Printf(say("o-ui HTTPS：%s（已显式允许非本机访问；请自行保护网络和订阅链接）\n", "o-ui HTTPS: %s (remote access explicitly allowed; protect your network and subscription links)\n"), access.Data.Origin)
	} else {
		fmt.Printf(say("o-ui 本机面板：%s://%s（首次使用请设置管理员；当前仅限本机）\n", "o-ui local panel: %s://%s (set up the administrator on first use; local access only)\n"), scheme, access.Data.Listen)
	}
	serve := server.ListenAndServe
	if panelTLS != nil {
		serve = func() error { return server.ListenAndServeTLS("", "") }
	}
	if err := serve(); err != nil && err != http.ErrServerClosed {
		log.Print(err)
		return 1
	}
	<-stopped
	return 0
}
