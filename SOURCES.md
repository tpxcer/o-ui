# 来源与独立实现记录

- 服务器安装器`install.sh`、离线设置`internal/localapi/panel_setup.go`与Release打包`scripts/build-release.mjs`由本项目独立实现，只调用系统工具（systemctl、runuser/setpriv、useradd、sysctl、ufw/firewall-cmd、tar、sha256sum）与本项目程序；不复制x-ui/3x-ui等面板安装脚本。证书申请在用户选择时下载并运行[acme.sh 3.1.6](https://github.com/acmesh-official/acme.sh/releases/tag/3.1.6)（GPL-3.0，独立外部工具，不进入发布包），参数依据其3.1.6源码选项（`--cert-profile`/`--certificate-profile`、`--standalone`需socat或python、默认CA为ZeroSSL故显式`--server letsencrypt`）、[dnsapi的dns_cf说明](https://github.com/acmesh-official/acme.sh/wiki/dnsapi)与[Profile selection](https://github.com/acmesh-official/acme.sh/wiki/Profile-selection)（IP证书需shortlived，短期证书需较小`--days`）；3.1.4起默认ARI、3.1.5起显式`--days`优先于ARI（见其Release说明）。Let's Encrypt IP证书约160小时有效（2026-01公开可用）。
- 首页公网信息与服务器套餐独立实现于`internal/localapi/public_ip.go`、`server_plan.go`、`internal/monitor`（网卡计数/开机时刻访问器）及`src/server-info.ts`、`server-plan.ts`、`server-plan-model.ts`，仅用Go/浏览器标准库与既有gopsutil。外部服务为用户允许的[ipinfo.io](https://ipinfo.io/)公开JSON接口（`https://ipinfo.io/json`、`https://v6.ipinfo.io/json`，2026-10-19核对字段ip/hostname/city/region/country/org，未使用令牌）；国家中文名用浏览器`Intl.DisplayNames`。无新依赖或SQL表。
- 核心安装持久化修复独立实现于`internal/core/manager.go`，只使用Go标准库文件Sync、目录Sync与原子重命名；返回是否已提交，避免把同步失败谎报为未更改。不复制上游/旧面板实现，不改依赖/SQLite/默认核心。`durable_integration_test.go`以本项目真实Handler/Runtime和官方26.7.28测试核心核原身份/订阅/流量恢复，专用临时ext4盘/Virtualization整客体直接stop复现及修复后通过，非硬件断电或全协议矩阵，详细证据见`artifacts/durable-recovery-local-20261019.md`。
- `internal/localapi/udp_integration_test.go`由本项目使用Go标准库独立实现回环SOCKS5 UDP关联/帧校验及回显目标，复用原`connections_integration_test.go`的网页导出与生命周期夹具；`traffic_udp_integration_test.go`另复用本项目Runtime/账本/限额逻辑，保持同一个UDP关联跨过到期/额度点。不复制旧面板或核心协议实现。控制代理、中继和目标均限制字面回环IPv4，测试只精确放行临时UDP目标端口，额度由真实回显产生而非伪造账本；无新增依赖、权限或产品策略变化，不将官方JSON的UDP结果泛化为第三方分享格式互通。
- 官方候选v26.10.17仅用户明确允许的临时本机验收，使用既有官方安装器，不加入产品依赖/分发包、不改默认26.7.28或现有核心选择。新增受限测试版本入口复用本项目原独立集成夹具与业务断言，不复制核心源码；官方Mac ARM64资产摘要57531c66df375da671f66cad1443f63e90410b83d09baf226e2f26f2748aae0f，实际程序92ccc93/Go1.27.2。测试与二进制安全扫描结果见最新验收记录，源码go.mod或“最新”标签不替代实际包证据，不据本机通过宣称生产安全或目标Linux已验。
- `src/core-compatibility.ts`及版本选择/实际操作的提示由本项目独立实现，仅使用本机已验26.10.17/Mihomo1.19.32两种REALITY结果；按实际候选/历史版本呈现，不复制上游实现、不修改握手策略、不把精确组合结论泛化所有版本。确定性最小控件测试不是原生浏览器/客户端验收，原真实连接证据见《验收记录.md》。

- 内部包许可收集独立实现于`scripts/third-party-notices.mjs`，复用Vite8.3.4的实际模块许可JSON和Go程序元数据，仅取已核版本及原文，不引入依赖或访问外站。保留Lucide/Feather与qrcode源注释、purego README/Go runtime及modernc继承汇总；dijkstrajs原MIT链接通知保留，另附`scripts/notices/dijkstrajs-MIT.txt`完整标准MIT文本（补充文件不是上游原文件）。Vite/Rolldown构建注入辅助代码、Go标准库及其vendor声明保守附带，原汇总中的工具/测试/素材不代表已链接。原文及覆盖JSON随包纳入摘要，未知/换版/替换或缺原文拒绝完成；不选择整体产品许可证，不将工程独立实现记录当法律保证。正式产品及Xray/Geo分发/源代码提供义务仍须另审。

- 本机构建及启动工具独立实现于`scripts/build-local.mjs`和`start-local.sh`，只调用现有npm构建/Go编译、标准库文件复制与SHA256、uname等系统校验工具，不引入新依赖或旧面板代码。默认宿主或显式macOS/Linux双架构目标，关闭CGO、PLATFORM及JSON清单记录同一目标；摘要通过后仍核平台，错包不执行。白名单复制程序资源/说明，拒绝符号链接，不带运行数据；明确绝对数据目录，只执行既有回环面板。当前仅个人内部测试目录，交叉编译不代表实机或生产验收，不上传或连接VPS，不是正式公开发行包；整体许可证及完整第三方许可提供义务仍待正式发行前核对。
- DoH夹具、固定外部查询与统计零值修复独立实现于`internal/localapi/doh_integration_test.go`、`doh_external_integration_test.go`及`internal/traffic`。行为核对官方固定26.7.28的[DoH传输](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/dns/nameserver_doh.go)与[统计协议](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/stats/command/command.proto)，省略value按protobuf默认0处理，不放宽显式非法值/双向完整性/回退检测。真实RuntimeAPI慢响应测试直接检查官方原统计与SQLite检查点。公共DoH仅用户明确授权的1.1.1.1/example.com A/AAAA，显式开关开启，非普通测试依赖；不复制核心实现或修改系统信任。

- 常用出站模型、表单与保护独立实现于`src/outbound-model.ts`、`src/xray-settings.ts`，只读核对官方固定26.7.28的[HTTP配置](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/http.go)、[SOCKS配置](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/socks.go)、[Freedom配置](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/freedom.go)及[Blackhole配置](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/blackhole.go)。表单使用单servers/单users形式；高级新版平铺地址/多参数不静默转换。`outbound_integration_test.go`通过Go标准库独立实现受限CONNECT/SOCKS5测试端点（只允许精确临时回环HTTP目标），实际核认证/出口/目标计数；不复制旧面板或上游代理实现，无新依赖/SQL/权限。

- 常用负载均衡表单及保留高级原文的模型独立实现于`src/balancer-model.ts`、`src/xray-settings.ts`，无新依赖或数据库字段。只读依据官方26.7.28的[配置接口](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/router.go)、[轮询与fallback依赖](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/router/balancing.go)、[随机策略](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/router/strategy_random.go)、[前缀选择与排序](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/proxyman/outbound/outbound.go)及[HTTP出站重试](https://github.com/XTLS/Xray-core/blob/v26.7.28/proxy/http/client.go)。自有Go CONNECT夹具仅访问精确回环目标，用实际出口计数验证分配，不复制上游实现、不启动观测或外站测速。

- REALITY最低版本策略只读依据官方[v26.7.28 transport_security.go](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/transport_security.go)及[安全策略提交](https://github.com/XTLS/Xray-core/commit/af7eb68028732a8ee3c0e5d6ab2b8a657bb2e770)、[维护者风险说明](https://github.com/XTLS/Xray-core/issues/6181#issuecomment-4567373533)。未填写默认26.3.27；Mihomo兼容布尔开关独立实现，只允许显式1.8.2，不复制上游实现、不改变默认。候选26.10.17实际链接的REALITY依赖[83f621788ca8/tls.go](https://github.com/XTLS/REALITY/blob/83f621788ca8/tls.go)216–235行在272行最低版本检查之前检查混合密钥份额；只读用于解释兼容边界，不复制实现，不把源码检查当本轮客户端KeyShare抓包证据。
- 第三方验收程序仅下载到临时目录，不进入产品依赖或分发包：[Hysteria2 app/v2.13.0](https://github.com/apernet/hysteria/releases/tag/app/v2.13.0)（旧apernet地址已有官方重定向），darwin-arm64程序SHA256 `c8c3589239ce4ecf335c07a3f6ec11b3ef8fbdfe9e5a5ea0ef8ba8ce50d2bb90`；[Mihomo v1.19.32](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.32)，官方darwin-arm64 gz SHA256 `3312a6780652c622890fd4357c6a853bbf865464fd047ac7b7f52dab8de18652`。摘要和自报版本已核，原URI/订阅及负向真实连接由本项目独立夹具验证。
- REALITY第五候选特斯拉`www.tesla.com:443`来自用户要求；与其他候选一样仅填写提示，非外站探测或可达性结论。

- 可选固定来源HTTPS管理/订阅独立实现于`panel_access.go`、`src/panel-access.ts`及既有启动/Host/Origin链路，仅Go标准库与现有SQLite文档/CAS，无新依赖/schema，不复制旧面板访问控制代码。用户明确授权仅新项目代码和本机测试，后续同范围直接推进；所有测试真实监听仍127.0.0.1，.invalid测试域经固定Dial直达回环，不读系统凭据或外部DNS、不改系统信任/防火墙/VPS。实际程序重启与固定官方26.7.28 HTTPS订阅对应代理链路分别验，不称公网部署完成。
- 产品需求：用户在本对话确认的独立重写、单 VPS 管理与“首页界面和 OUI 一样”。
- 用户追加确认台湾VPS独立目录/回环临时端口/临时数据隔离测试，不是生产更新授权。Linux静态编译及既有测试入口独立实现，`PANEL_PROCESS_QA_BINARY`仅为无开发工具环境复用同一实际面板测试；没有安装远端开发工具或引入旧OUI源码，完整证据见`artifacts/linux-isolation-20261018.md`。
- 范围裁剪：用户在查看原型后明确取消“节点”和“信息预览”两个独立功能；不影响首页服务器信息与入站创建方向。
- 首页追加裁剪：用户要求取消独立管理卡片，五项操作并入独立面板卡片，检测更新移至侧栏开发状态原位置。
- 入站/客户端流程来自本对话使用要求及项目记忆中的命名/嗅探保护项；模型、表单、中文日历与SQLite草稿API均在本项目独立实现，不复制旧库结构或认证逻辑。
- 视觉参考：2026-10-18 用户主动打开的当前 OUI 首页，只读查看布局与可见交互入口；未读取、复制旧前端源码、样式文件或图标文件来实现此原型。生产 IP、主机名和套餐值不进入演示数据。
- 设置参考：前期因Mac锁定未能查看；用户解锁后已只读观察当前Chrome OUI六分类、基础下统计/日志/基本路由和面板常规的证书字段。仅参考可见字段与使用效果，不导入生产证书/配置，不保存或重启旧面板。证书为两个绝对路径输入；新表单/证书API/持久化/复用均独立实现，未引入外部VPN或服务商集成。
- 诊断与操作摘要由本项目`internal/diagnostics`独立实现，只用Go标准库；有界分行、固定代码白名单及内存环形缓冲，不复制旧面板日志读取/脱敏代码。客户端日志弹窗独立实现，原文不进入网页，不读外部日志文件。
- 单客户端本机订阅独立实现于`internal/localapi/subscriptions.go`和`src/subscriptions.ts`，仅Go标准crypto/rand、SHA256、恒时比较、Base64与已有SQLite文档/CAS；复用本项目已应用连接导出，不复制旧面板订阅或认证代码，无新增依赖。Vite仅新增本机代理与错误地址脱敏，真实官方连接验收与浏览器凭证操作边界分别记录。
- 常用DNS表单/无损准入模型独立实现于`src/dns-model.ts`与`src/xray-settings.ts`，无新增依赖/SQL/权限；只读核对官方固定[v26.7.28 DNS配置](https://github.com/XTLS/Xray-core/blob/v26.7.28/infra/conf/dns.go)、[服务器创建](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/dns/nameserver.go)及[查询与缓存](https://github.com/XTLS/Xray-core/blob/v26.7.28/app/dns/dns.go)。不复制旧面板DNS实现；`dns_integration_test.go`用Go标准库构造仅回答固定.invalid域名的自有回环TCP DNS服务，真实官方出站/显式测试CA/最窄临时端口放行，不调用公共解析器。
- 嗅探排除字段依据官方v26.7.28 `infra/conf/xray.go`的ipsExcluded/domainsExcluded；REALITY候选只是常用填写提示，不是探测结果或网站可用保证。面板HTTPS来自Go标准库crypto/tls、x509与net/http；生命管道/专属进程组/Linux父死信号独立实现，不复制旧面板监督代码。
- 原型代码：在本目录独立编写，用户2026-10-19选定以GPL-3.0发布（根目录`LICENSE`为gnu.org官方文本，SHA256 `3972dc97…986`）；此前接触过旧源码，因此不宣称严格洁净室开发或作法律独立保证。
- 图标：`lucide`（ISC），从 npm 安装，不复制旧面板图标资源。
- 系统资源采集：`github.com/shirou/gopsutil/v4 v4.26.9`（BSD-3-Clause）及其平台依赖，锁定于go.mod/go.sum；本机CPU/容量/主机/负载/网卡及本次实际核心PID的RSS/线程只读采样，不对外查询IP、不搜索/接管其他核心。Linux套接字用受限PID的/proc与库接口过滤；macOS以系统/usr/sbin/lsof数字字段独立解析，避免库吞诊断错误后误报0。完整依赖许可仍须随未来发行包提供。
- 指标语义核对：官方v26.7.28 `app/stats/command/command.proto`中SysStats.NumGoroutine/Alloc分别为协程/堆，不冒充系统线程/RSS；网络指标按实际OS套接字语义独立实现，不复制旧面板。
- 二维码：`qrcode`1.5.4（MIT）与类型定义1.5.6（MIT），仅本机编码；测试解码`jsqr`1.4.0（Apache-2.0），不进入生产构建。许可与精确依赖见package-lock.json及各包LICENSE。
- 开发工具：Vite（MIT）、TypeScript（Apache-2.0）、Node.js 类型定义（MIT）；精确版本以 `package-lock.json` 为准，各依赖许可保留在安装包中。
- 无外部图片、字体、CDN或分析脚本。官方版本查询和包下载由Go后端访问GitHub，不传用户业务配置；无第三方镜像或额外账号。
- 核心版本管理代码在本项目独立编写，仅使用Go标准库；格式与发布元数据来自官方GitHub，不参考旧面板实现。
- 业务备份格式/预览与事务恢复独立实现；AES/GCM来自Go标准库，scrypt来自现有`golang.org/x/crypto v0.58.0`（BSD-3-Clause），没有新引入密码学依赖或自行实现算法。包不包含管理员/会话/核心，不参考旧面板备份逻辑。
- Xray不嵌入前端构建，由用户选择时从官方发布包下载。保存随包LICENSE（MPL-2.0）与geoip/geosite数据；分发正式产品时还需核对完整许可及源代码提供义务，本轮没有发布构件。
- 默认官方版本：[v26.7.28](https://github.com/XTLS/Xray-core/releases/tag/v26.7.28)（prerelease=true）。本机macOS ARM64原始包SHA256：`9b99a351febe31b7e0c7f22deeb1577a1da0b98aaa51aec7fd17832e68cf63d6`，来源为官方tag API的asset.digest，并已下载实测匹配。
- 官方版本API：`https://api.github.com/repos/XTLS/Xray-core/releases`；官方资产下载入口：`https://github.com/XTLS/Xray-core/releases/download/`，仅允许官方GitHub资产CDN重定向。此来源校验不是独立发布者签名或每协议兼容性证明。
- 六分类设置与首页弹窗参考用户所供截图的可见结构，独立编写HTML/CSS/TypeScript；不复制旧实现。字段依据固定官方v26.7.28的`infra/conf/freedom.go`、`router.go`、`log.go`、`dns.go`；Freedom生成`targetStrategy`并消除旧字段冲突。
- 常用路由模型/表单与未编辑JSON片段保留在本项目独立实现，无新增依赖；字段与先命中规则依据同版官方router.go/路由接口，通过官方导出客户端的5个实际回环场景验证。不复制旧OUI基本路由开关逻辑，不据此加入外部VPN或Geo资源管理。
- 入站纯编译依据官方v26.7.28的`infra/conf/vless.go`、`hysteria.go`、`transport_internet.go`、`transport_security.go`、`transport_method.go`，不引入旧面板代码或Xray源码依赖；仅按公开JSON接口独立实现。固定虚构测试输入不代表已通过官方预检或客户端兼容认证。
- SQLite驱动：modernc.org/sqlite v1.60.1（BSD-3-Clause，附带SQLite及依赖许可）；密码哈希：golang.org/x/crypto v0.58.0（BSD-3-Clause）。直接/间接版本见go.mod/go.sum，发布前还需完整许可清单；当前无发布包。
- 实际XHTTP-TLS测试基于官方`transport_security.go`的certificateFile/usage=verify及`proxy/freedom/freedom.go`、`infra/conf/freedom.go`的私网保护/finalRules；只在隔离测试配置精确放行回环HTTP目标，未复制官方实现到业务代码。
- 配置应用、凭据持久化、证书快照、官方CLI预检与子进程生命周期均由本项目独立实现（internal/localapi/runtime.go、compile_settings.go、internal/engine）；不读取旧面板实现。密码/身份生成使用Go标准密码学库，核心只通过官方JSON及CLI接口调用。
- 跨重启已应用历史与显式恢复独立实现于`runtime_history.go`：标准库SHA256/文件同步、已有SQLite文档CAS与官方CLI预检；不增加依赖或复制旧面板恢复代码。真实进程测试只复制已校验官方包到临时目录，不改变原包选择。
- 离线面板访问恢复独立实现于`panel_recovery.go`与本项目CLI：复用既有运行目录Flock、普通文件检查/私有备份和SQLite两文档事务CAS，不新增依赖/schema或引用旧面板实现；明确停服并确认才停用TLS/恢复回环，不重置账号/业务/核心或启动服务。备份只存路径设置及修订，不复制证书内容；不是业务导入或自动续签。
- Linux ARM64本机验收工具采用macOS系统Virtualization/Swift API，默认只有只读启动介质/无网卡的内存客体；持久化专项可显式增加固定文件名/256MiB/普通非链接的专用临时测试盘，SIGUSR1只直接stop本runner客体。产品不依赖该框架；只读借用已有Alpine介质，不运行或修改其它项目磁盘。测试重放宿主经HTTPS取得的官方GitHub原始发布元数据和zip，生产安装器的来源字段/SHA/解压/版本探测仍保持；不向产品加入缓存来源或跳过校验。相关脚本/测试不随内部运行包分发。
- 统计接口依据固定26.7.28官方`infra/conf/api.go`、`app/commander/commander.go`（Unix listen）、`main/commands/all/api/stats_query.go`/`shared.go`（非reset CLI/JSON）、`app/stats/command/command.proto`（int64计数）。独立实现`internal/traffic`累计账本及`internal/localapi/traffic.go`检查点/限制重载，不复制官方或旧面板统计实现，不新增gRPC依赖。
- 客户端导出依据固定26.7.28官方REALITY/TLS/VLESS/Hysteria JSON接口：REALITY客户端password为公钥，分享URI为pbk；HY2结构依据hysteria.go/transport_method.go。HY2 URI公开规范：https://v2.hysteria.network/docs/developers/URI-Scheme/ 。独立编写导出/转义逻辑，不复制其他面板分享实现；官方JSON真实连接与URI/二维码编码检查分别验收。
- HY2限制验收的对端关闭边界核查：官方v26.7.28 `transport/internet/hysteria/hub.go`、`dialer.go`（默认空闲30秒）及其依赖apernet/quic-go固定提交`6c6cc9bcb716`的`transport.go`、`connection.go`（Transport.Close以destroy关闭、不发送对端错误）。只用于区分服务端停止转发与客户端感知断线，不引入/修改这两项目源码或更改默认核心版本。
