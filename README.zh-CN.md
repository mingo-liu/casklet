# mini-docker

一个面向 macOS 的命令行容器工具，使用自行实现的 Go 容器引擎。`mdocker`
自动管理专用的 Lima Linux 虚拟机，容器通过 mini-docker 的命名空间、cgroups v2
和进程监督机制在虚拟机中运行。无需 Docker Engine 或 containerd。

支持的宿主平台：macOS 13.5 及以上版本，支持 Apple 芯片和 Intel，要求 Lima 2.0
及以上版本。Linux 可执行文件是虚拟机内部组件，不作为独立的宿主平台产品提供支持。
请运行可信工作负载；这个学习用途的运行时不为不可信代码提供安全保证。

## 快速开始

```sh
brew install lima
make build
sudo make install

mdocker doctor
mdocker run -- /bin/sh -c 'hostname; ps; echo hello'
mdocker run -it -- /bin/sh
```

也可以直接使用 `./bin/mdocker`，无需安装。运行时使用普通 Mac 用户，不要加 `sudo`。
首次执行需要容器引擎的命令时，会自动创建 `mini-docker-runtime` 虚拟机、安装随程序
打包的引擎，并准备静态 BusyBox 文件系统。首次初始化需要联网，后续运行会使用已有
虚拟机及其本地数据。容器选项放在必需的 `--` 分隔符之前。

## 常用操作

```sh
mdocker run -d --name worker --memory 128m --pids-limit 64 --cpus 0.5 \
  -- /bin/sleep 300
mdocker ps
mdocker exec worker -- /bin/sh -c 'hostname; id'
mdocker logs --tail 20 worker
mdocker inspect worker
mdocker stats worker
mdocker stop worker
mdocker rm worker
```

Mac 用户主目录会共享给虚拟机，用于导入 rootfs 和绑定挂载。容器及导入的镜像保存在
虚拟机的 Linux 磁盘中。发布的 TCP 和 UDP 端口会转发到 Mac。默认网络仅启用回环接口；
需要网络连接和端口发布时，使用 `--network bridge`。

```sh
mdocker run --mount "type=bind,source=$PWD,target=/work" --workdir /work \
  -- /bin/sh -c 'ls'
mdocker machine status
mdocker machine stop
mdocker machine start
```

虚拟机默认配置为 4 个 CPU、4 GiB 内存和 20 GiB 磁盘。首次使用前，可以自定义资源
配置并添加共享目录：

```sh
mdocker machine init --cpus 2 --memory 2 --disk 20 --mount /Volumes/Projects
```

## 文档与开发

- [使用指南](guides/usage.md)：文件、镜像、网络、安全与生命周期管理（英文）。
- [开发指南](guides/development.md)：构建、测试与故障排查（英文）。
- [架构说明](ARCHITECTURE.md)：宿主机与虚拟机的边界及资源归属（英文）。
- `mdocker help`：命令语法与选项。

```sh
make fmt-check test vet test-race
make test-macos # 在运行时虚拟机中执行真实命令；需要时会自动创建虚拟机。
```

内部 Linux 引擎保留了需要特权的集成测试，在另一台专用开发虚拟机中运行。
生成的二进制文件、文件系统模板、运行时状态以及本地 `docs/` 笔记不纳入版本控制。
许可证见 [LICENSE](LICENSE)。英文版见 [README.md](README.md)。
