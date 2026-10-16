# casklet

[English](README.md) · [简体中文](README.zh-CN.md)

面向 macOS 的命令行容器工具，使用自行实现的 Go 引擎。casklet 自动管理专用的
Lima Linux 虚拟机，通过 Linux 命名空间、cgroups v2 和进程监督机制运行容器，
无需 Docker Engine 或 containerd。

要求 **macOS 13.5+**（Apple 芯片或 Intel）和 **Lima 2.0+**。Linux 引擎仅作为
虚拟机内部组件，不单独支持 Linux 宿主平台。这是一个学习用途的运行时，请仅运行
可信工作负载；它不为不可信代码提供安全保证。

## 安装

```sh
brew install lima
```

从[版本页面](https://github.com/mingo-liu/casklet/releases)将对应架构的安装包和
`SHA256SUMS` 下载到空目录。将下面的版本号改为实际下载版本；Apple 芯片使用
`arm64`，Intel 使用 `amd64`。校验显示 `OK` 后再继续安装：

```sh
CASKLET_VERSION=v0.1.0
CASKLET_ARCH=arm64
shasum -a 256 --ignore-missing -c SHA256SUMS &&
  tar -xzf "casklet_${CASKLET_VERSION}_darwin_${CASKLET_ARCH}.tar.gz" &&
  sudo install -d /usr/local/bin &&
  sudo install -m 0755 casklet /usr/local/bin/casklet
casklet --version
```

客户端内置匹配的 Linux 引擎，无需 Go 或 Make。发布的程序未经 Developer ID
签名或公证，通过浏览器下载后，macOS 可能要求在**系统设置 > 隐私与安全性**中批准运行。

也可以安装 **Go 1.27.2+** 和 Make，在仓库目录中从源码构建：

```sh
make build
sudo make install
```

无需安装也可直接运行 `./bin/casklet`。更新时重复所选安装方式即可；下次执行需要
引擎的命令时，虚拟机中的引擎会自动更新，并保留容器和镜像。

## 快速开始

使用普通 Mac 用户运行 casklet，**不要加 sudo**：

```sh
casklet doctor
casklet run -- /bin/sh -c 'hostname; ps; echo hello'
casklet run -it -- /bin/sh
```

首次执行需要引擎的命令时，会自动创建 `casklet-runtime` 虚拟机，准备引擎和
BusyBox 文件系统。首次初始化需要联网，后续复用虚拟机及本地数据；初始化失败后，
恢复网络并重试即可。

运行应用，并将端口发布到 Mac 的本地回环地址：

```sh
casklet run -d --name redis --network bridge -p 127.0.0.1:6379:6379 --image redis:8
casklet ps
casklet logs --tail 20 redis
casklet exec -it redis -- /bin/sh
```

使用 `exit` 退出 exec shell，Redis 会继续运行。OCI/Docker 镜像按本机对应的
Linux 架构自动拉取，缓存后可离线使用；`image pull` 可刷新缓存的标签版本。
显式命令参数放在 `--` 后；使用镜像默认启动命令时，可省略命令和分隔符。

默认网络仅支持容器内回环通信。使用 `--network bridge` 联网和发布 TCP/UDP 端口，
或使用命名网络实现隔离的服务发现。OCI 镜像暂不支持 rootless 运行。

## 管理容器与数据

```sh
casklet inspect redis
casklet stats redis
casklet stop redis
casklet start redis
casklet restart redis
casklet stop redis
casklet rm redis
```

管理命令接受容器名称、完整 ID 或唯一 ID 前缀。停止、启动和重启会保留容器私有文件，
`rm` 会删除这些文件；绑定挂载和命名数据卷独立于容器保留数据。Mac 用户主目录默认
与虚拟机共享，其他目录需要显式添加共享。

```sh
casklet run --mount "type=bind,source=$PWD,target=/work" --workdir /work -- /bin/ls
casklet volume create app-data
casklet run --mount type=volume,source=app-data,target=/data -- /bin/sh -c 'echo saved > /data/message'
```

通过[项目配置](guides/usage.md#project-deployment)部署多个服务，声明网络、数据卷
和启动依赖：

```sh
casklet project up --config project.json --wait --timeout 1m
casklet project ps demo
casklet project down demo
```

此示例使用指南中的 `demo` 配置。`project down` 默认保留数据卷；添加 `--volumes`
也会删除项目拥有的数据卷。

## 功能概览

完整示例、默认值和限制见[使用指南](guides/usage.md)（英文）。

| 类别 | 支持的功能 |
| --- | --- |
| 镜像 | [OCI/Docker 拉取与本地目录导入](guides/usage.md#ocidocker-images)、[私有仓库与 macOS 钥匙串凭据](guides/usage.md#registry-credentials)、[离线 OCI 归档](guides/usage.md#offline-oci-image-archives) |
| 执行 | [CPU、内存、进程数和临时存储限制，环境变量、用户与只读根目录](guides/usage.md#resource-and-execution-options)、[交互式 run/exec](guides/usage.md#terminals-and-exec) |
| 生命周期 | [日志与诊断](guides/usage.md#background-lifecycle-and-diagnostics)、[重启策略](guides/usage.md#automatic-restarts)、[健康检查](guides/usage.md#container-health-checks)、[实时资源监控](guides/usage.md#multi-container-resource-monitoring) |
| 网络 | [TCP/UDP 端口发布](guides/usage.md#ipv4-networking)、[隔离的命名网络与服务发现](guides/usage.md#named-networks-and-service-discovery) |
| 存储 | [绑定挂载与命名数据卷](guides/usage.md#persistent-data)、[数据卷备份与迁移](guides/usage.md#volume-backup-restore-and-migration)、[磁盘占用与镜像清理](guides/usage.md#disk-usage-and-cache-cleanup)、[下载缓存限制](guides/usage.md#shared-download-cache) |
| 部署 | [JSON 配置与环境变量文件](guides/usage.md#repeatable-deployment-configuration)、[标签与批量操作](guides/usage.md#labels-filters-and-batch-lifecycle-operations)、[多服务项目与指定服务替换](guides/usage.md#project-deployment) |
| 隔离 | [安全控制与 rootless 限制](guides/usage.md#security-and-rootless-execution) |

## 虚拟机

虚拟机默认配置为 **4 个 CPU、4 GiB 内存和 20 GiB 磁盘**，容器、镜像和数据卷
保存在虚拟机磁盘中。查看状态或启停虚拟机：

```sh
casklet machine status
casklet machine stop
casklet machine start
```

停止虚拟机会停止工作负载，但保留数据。启动时，采用 `always` 或满足条件的
`unless-stopped` 策略的容器会自动恢复；其他保留的容器用 `casklet start NAME` 启动。

首次使用前，可通过 `machine init` 自定义资源和共享目录；已有虚拟机可停机后通过
`machine resize` 调整资源，磁盘只能扩容。详见[虚拟机配置](guides/usage.md#macos-execution-environment)。

## 文档与开发

- `casklet help` 列出命令；`casklet COMMAND --help` 查看选项与示例。
- [使用指南](guides/usage.md)：详细用法与限制（英文）。
- [开发指南](guides/development.md)：构建、测试、故障排查与发布（英文）。
- [架构说明](ARCHITECTURE.md)：模块边界与资源归属（英文）。

```sh
make fmt-check test vet test-race
make test-macos
```

特权 Linux 测试共用 `casklet-runtime` 虚拟机，必须遵循
[保留日常数据的独占测试流程](guides/development.md#internal-linux-engine-tests)。
反馈问题时请附上 `casklet version --json` 输出；查询无需 Lima 或运行中的虚拟机。

许可证见 [LICENSE](LICENSE)。
