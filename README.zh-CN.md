# casklet

[English](README.md) · [简体中文](README.zh-CN.md)

一个面向 macOS 的命令行容器工具，使用自行实现的 Go 容器引擎。`casklet`
自动管理专用的 Lima Linux 虚拟机，容器通过 casklet 的命名空间、cgroups v2
和进程监督机制在虚拟机中运行。无需 Docker Engine 或 containerd。

支持的宿主平台：macOS 13.5 及以上版本，支持 Apple 芯片和 Intel，要求 Lima 2.0
及以上版本。Linux 可执行文件是虚拟机内部组件，不作为独立的宿主平台产品提供支持。
请运行可信工作负载；这个学习用途的运行时不为不可信代码提供安全保证。

## 快速开始

从源码构建需要 Go 1.27.1 及以上版本和 Make。请在仓库目录中执行：

```sh
brew install lima
make build
sudo make install

casklet doctor
casklet run -- /bin/sh -c 'hostname; ps; echo hello'
casklet run -it -- /bin/sh
```

也可以直接使用 `./bin/casklet`，无需安装。运行时使用普通 Mac 用户，不要加 `sudo`。
首次执行需要容器引擎的命令时，会自动创建 `casklet-runtime` 虚拟机、安装随程序
打包的引擎，并准备静态 BusyBox 文件系统。首次初始化需要联网，后续运行会使用已有
虚拟机及其本地数据。指定容器命令时，选项放在 `--` 分隔符之前。仓库镜像提供默认
启动命令，缓存未命中时会自动下载；使用镜像默认命令时不需要 `--`。

更新源码后，再执行 `make build` 和 `sudo make install` 更新已安装的客户端。
下次执行需要引擎的命令时，会自动更新其内置的虚拟机引擎，保留现有容器和镜像。

## 运行应用镜像

```sh
casklet run -d --name redis --image redis:8
# Publish to localhost on your Mac:
casklet run -d --name redis-local --network bridge -p 127.0.0.1:6379:6379 --image redis:8
```

OCI/Docker 镜像包含应用程序、依赖和默认启动配置。虚拟机会选择匹配的 Linux 架构，
校验并解包镜像各层，再通过 casklet 自身的引擎运行。缓存的镜像可以离线使用。
使用 `--env` 提供应用配置、`--mount` 绑定持久化目录，在 `--` 后指定命令参数。
这些参数会替换镜像的 `Cmd`，保留 `Entrypoint`；使用 `--entrypoint` 可以覆盖入口。
目前支持允许匿名访问的仓库，暂不支持私有仓库登录和 OCI 镜像的 rootless 运行。
详细说明见[镜像指南](guides/usage.md#ocidocker-images)（英文）。

后台容器继承镜像的 `Healthcheck` 配置，可用 `--health-cmd` 覆盖探针，
或用 `--no-healthcheck` 禁用。`ps` 和 `inspect` 单独显示健康状态；探针失败
不会触发自动重启。时序与就绪语义见[健康检查指南](guides/usage.md#container-health-checks)（英文）。

应用配置也可以通过 `run --config redis.json` 和 `run`/`exec` 的可重复
`--env-file application.env` 读取。JSON 使用长选项名称，命令行标量覆盖配置文件，
显式 `--env` 覆盖环境变量文件。文件在 Mac 本地读取。详见
[部署配置](guides/usage.md#repeatable-deployment-configuration)（英文）。

## 拉取进度与镜像管理

自动拉取和显式 `image pull` 都会在 stderr 显示逐层下载、校验和解包进度。
`--progress=auto`（默认）在终端中动态刷新进度条，重定向时输出纯文本；`plain` 和
`tty` 可以强制选择对应模式。镜像 ID、容器 ID 和应用输出保留在 stdout。
缓存命中时显示 `Using cached image`；显式拉取未变化的镜像时显示 `Image is up to date`。

```sh
casklet image pull redis:8
casklet image pull --progress=plain redis:8
casklet image ls
casklet image ls --json
```

标签会继续使用缓存版本，直到执行 `image pull` 刷新。使用仓库摘要可以固定镜像来源。
删除镜像时，将下面的 `IMAGE_ID` 替换为 `image ls` 中的完整本地 `sha256:` ID。
需要先停止并删除所有引用该镜像的容器；删除命令接受本地 ID，不接受 `redis:8` 等仓库名称。

```sh
casklet image rm IMAGE_ID
```

## 常用操作

```sh
casklet run -d --name worker --memory 128m --pids-limit 64 --cpus 0.5 \
  -- /bin/sleep 300
casklet ps
casklet exec worker -- /bin/sh -c 'hostname; id'
casklet exec -it worker -- /bin/sh
casklet logs --tail 20 worker
casklet inspect worker
casklet stats worker
casklet stop worker
casklet start worker
casklet restart worker
casklet stop worker
casklet rm worker
```

输入 `exit` 可以退出 exec 终端，容器主进程继续运行。停止后启动及重启都会保留容器
内部文件；`rm` 会删除这些文件。绑定挂载的宿主机数据在删除容器后仍然保留。

Mac 用户主目录会共享给虚拟机，用于导入 rootfs 和绑定挂载。容器及导入的镜像保存在
虚拟机的 Linux 磁盘中。镜像缓存位于虚拟机内的 `/var/lib/casklet/images`，容器记录
及独立文件系统位于 `/var/lib/casklet/containers`。发布的 TCP 和 UDP 端口会转发到
Mac。默认网络仅启用回环接口；需要网络连接和端口发布时，使用 `--network bridge`。

```sh
casklet run --mount "type=bind,source=$PWD,target=/work" --workdir /work \
  -- /bin/sh -c 'ls'
casklet machine status
casklet machine stop
casklet machine start
```

停止虚拟机会停止其中的工作负载，但保留其数据。启动虚拟机后，采用 `always`
或满足条件的 `unless-stopped` 策略的容器会自动恢复；其他保留的容器可以用
`casklet start NAME` 再次启动。

虚拟机默认配置为 4 个 CPU、4 GiB 内存和 20 GiB 磁盘。首次使用前，可以自定义资源
配置并添加共享目录：

```sh
casklet machine init --cpus 2 --memory 2 --disk 20 --mount /Volumes/Projects
```

命名数据卷将数据保存在 VM 磁盘上，独立于容器：

```sh
casklet volume create app-data
casklet run --mount type=volume,source=app-data,target=/data -- /bin/sh -c 'echo saved > /data/message'
casklet volume ls
```

新卷为空且属于 root，容器删除后数据仍保留，目前不支持用户命名空间。
移除所有引用该卷的容器后，`casklet volume rm app-data` 才能删除卷和数据。

停止使用卷的工作负载后，可用 `casklet volume export app-data > app-data.tar` 备份，
再用 `casklet volume restore app-restored < app-data.tar` 恢复到新卷。tar 流也可迁移到
另一台 Mac。限制与一致性说明见[卷备份指南](guides/usage.md#volume-backup-restore-and-migration)（英文）。

查看 VM 磁盘占用，预览或清理未使用的缓存镜像：

```sh
casklet system df
casklet image prune --dry-run
casklet image prune
```

清理包括未使用的带标签镜像，保留容器与数据卷，并跳过存在活动租约或被保留容器引用的镜像。

镜像的 `StopSignal` 会保留用于后台容器关闭，可用 `--stop-signal SIGQUIT` 覆盖。
停止和重启会先发送该 Linux 信号，超过配置的停止宽限时间后强制终止。
前台收到的外部信号仍按原信号转发。

后台容器支持自动重启：

```sh
casklet run -d --name service --restart unless-stopped -- /bin/sleep 300
casklet run -d --name retry-job --restart on-failure:3 -- /bin/sh -c 'exit 1'
```

默认为 `no`。`on-failure[:1-1000]` 重试非零或未知的退出状态；`always` 与
`unless-stopped` 也会在正常退出后重启，并在 VM 启动后恢复。手动停止会暂停自动重启，
直到再次 start/restart；`always` 在下一次 VM 启动时恢复，`unless-stopped` 则保持停止。
重试采用从 1 秒到 30 秒的退避。自动重启的端口转发依赖 Lima，无法实时预检 Mac
端口占用。重试计数与检查字段的详情见使用指南。

## 文档与开发

- [使用指南](guides/usage.md)：文件、镜像、网络、安全与生命周期管理（英文）。
- [开发指南](guides/development.md)：构建、测试与故障排查（英文）。
- [架构说明](ARCHITECTURE.md)：宿主机与虚拟机的边界及资源归属（英文）。
- `casklet help`：命令概览；`casklet COMMAND --help`：具体命令的语法、选项与示例。

```sh
make fmt-check test vet test-race vuln
make test-macos # 在运行时虚拟机中执行真实命令；需要时会自动创建虚拟机。
```

内部 Linux 引擎保留了需要特权的集成测试，在另一台专用开发虚拟机中运行。
生成的二进制文件、文件系统模板、运行时状态以及本地 `docs/` 笔记不纳入版本控制。
许可证见 [LICENSE](LICENSE)。
