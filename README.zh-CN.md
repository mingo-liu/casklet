# mini-docker

一个使用 Go 编写的小型容器运行时，支持 Linux arm64 和 amd64。在独立的 PID、挂载、UTS、IPC 和网络命名空间中运行前台或后台命令，使用复制的 BusyBox 根文件系统，并通过 cgroups v2 限制内存、进程数量，以及可选的 CPU 使用量。

目前已实现前台执行、交互式终端、后台容器管理、容器内交互式命令执行、检查、资源统计、目录绑定挂载、本地镜像和生命周期扩展，并在专用 Linux VM 中完成验证。执行选项支持配置环境变量、工作目录、数字用户身份、只读根文件系统，以及内容寻址的本地镜像。

## 环境要求

使用专用的 Ubuntu 24.04 开发 VM，具备 systemd、Linux 6.8 或更新版本、cgroups v2、root 权限、Go 1.25 或更新版本，以及 `busybox-static`。必需的 cgroup 接口包括 `memory`、`pids`、`memory.swap.max` 和 `cgroup.kill`。前台执行使用获得 cgroup 委派授权的 systemd scope；后台执行会创建自己的委派服务。后台管理需要 `/usr/bin/systemd-run` 和 `/usr/bin/systemctl`。

此运行时适合运行可信程序。命令默认以容器 UID 0 运行，并削减 capabilities；可通过 `--user` 指定其他数字身份。它不为不可信代码提供安全保证。目前不支持镜像仓库、托管数据卷。

## 在 macOS 上使用开发 VM

安装 Lima，然后启动专用 VM：

```sh
brew install lima
limactl start --name mini-docker dev/lima.yaml
```

配置会安装 BusyBox、构建工具，以及从 [Go 官方下载页面](https://go.dev/dl/) 获取并经过校验和验证的 Go 1.27.1。[Lima plain 模式](https://lima-vm.io/docs/config/plain/) 禁用宿主机文件系统共享，因此需要将源码快照传入 VM 内可写的本地目录。在仓库根目录运行：

```sh
set -o pipefail
./scripts/snapshot.sh | limactl shell mini-docker sh -c \
  'source_dir=$(mktemp -d "$HOME/mini-docker-source.XXXXXXXX") &&
    tar -xzf - -C "$source_dir" && printf "Source directory: %s\n" "$source_dir"'
limactl shell mini-docker
# Replace the suffix with the exact directory printed by the transfer.
cd ~/mini-docker-source.XXXXXXXX
make build
make rootfs
sudo make install
```

将示例中的目录后缀替换为传输命令实际输出的目录。修改源码后，重新传输并使用新输出的目录。快照脚本使用 NUL 分隔的文件名，包含当前存在的已跟踪文件和未被忽略的文件。每次传输都使用新目录，已删除的源码不会残留在后续构建中。旧快照会保留，直到显式删除。构建产物、rootfs 模板和运行时状态均保存在 VM 自己的文件系统中，不会将宿主机目录挂载进 VM。如果使用已有的 Ubuntu VM，请先通过 `apt-get` 安装 `busybox-static binutils make`，并安装 Go 1.25 或更新版本，再执行下面的命令。

## 运行

```sh
mdocker doctor --rootfs ./rootfs/busybox
mdocker run --rootfs ./rootfs/busybox \
  --hostname mini --memory 128m --pids-limit 64 --timeout 30s \
  -- /bin/sh -c 'hostname; ps; echo hello'
printf 'hello\n' | mdocker run \
  --rootfs ./rootfs/busybox -- /bin/cat
```

`make build` 生成 `bin/mdocker`；构建完成后，`sudo make install` 将其安装到 Linux VM 内的 `/usr/local/bin/mdocker`。安装后可在该 Linux 主机的任意目录运行 `mdocker`，也可以不安装，直接使用 `./bin/mdocker`。通过 `make install PREFIX=/your/prefix` 可指定其他安装前缀，请确保该前缀下的 `bin` 目录位于 `PATH` 中。

CLI 在需要 root 权限时会自动使用 `sudo`。如果当前进程尚未处于合适的委派 scope 中，前台运行和 `doctor` 会创建获得委派授权的 `systemd-run --scope`。后台运行创建自己的委派服务；管理命令和镜像命令不会创建前台 scope。标准输入、终端处理和命令退出状态会正常传递。帮助命令和参数错误不会调用 `sudo`。这是一个 Linux CLI；在 macOS 上请先通过 `limactl shell mini-docker` 进入 VM。

必须使用 `--` 分隔运行时选项和容器命令，后面的参数会被直接执行；需要 shell 语法时，请显式调用 `/bin/sh -c`。旧的 `scripts/run-linux.sh` 仍作为兼容启动器保留，使用 `sudo systemd-run --scope`，并支持通过 `MINI_DOCKER_BINARY` 选择其他可执行文件；`bin/mini-docker` 是指向 `mdocker` 的兼容符号链接。

默认值：主机名 `mini`、内存 `128m`、进程/线程上限 `64`、超时 `0`（不限制）、停止宽限期 `5s`。内存后缀 `k`、`m` 和 `g` 使用 1024 的幂。每个新容器都会复制 rootfs 模板，并挂载独立的 `/proc` 和临时存储。前台执行退出后会删除工作文件系统；后台容器保留自己的私有副本，直到执行 `rm`。显式配置的目录绑定挂载会将数据保留在 Linux 主机的源目录中。网络仅包含回环接口。默认情况下，命令使用固定环境变量，并从 `/` 开始执行。

### 资源与执行选项

```sh
mdocker run --rootfs ./rootfs/busybox \
  --cpus 0.5 --env MODE=demo --env EMPTY= --workdir /tmp \
  --user 1000:1000 --read-only \
  -- /bin/sh -c 'id; pwd; echo "$MODE"; echo hello > result; cat result'
```

| 选项 | 行为 |
| --- | --- |
| `--cpus` | `0` 表示不限制；接受 `0.01` 到 `1000`，最多三位小数。以 100ms 为周期限制整个容器的 CPU 带宽；`0.5` 表示允许使用半个 CPU 的计算量。仅在请求限制时要求 CPU 控制器已获委派。 |
| `--env KEY=VALUE` | 可重复指定以添加变量；同名变量以最后一次赋值为准。允许空值以及值中包含 `=`。名称可包含字母、数字和下划线，但不能以数字开头。可覆盖固定的默认值 `PATH=/bin:/usr/bin`、`HOME=/` 和 `LANG=C`，不会继承宿主环境变量。 |
| `--workdir /PATH` | 使用容器内已存在的目录，默认 `/`。目录不存在或无法访问时启动失败。命令查找使用容器内配置的 `PATH` 和工作目录。 |
| `--user UID[:GID]` | 使用 `0` 到 `4294967294` 的数字 ID；GID 默认与 UID 相同。启动命令前会清空补充组及所有 capability 集合。容器 init 使用相同身份监督后代进程。 |
| `--read-only` | 将复制的根文件系统挂载为只读。`/tmp` 仍是可写且有大小限制的 tmpfs；最小化的 `/dev` 仍可使用。绑定挂载保留各自的只读设置。默认根文件系统可写；后台容器会保留它直到被删除。 |
| `--stop-timeout DURATION` | 收到信号、发生超时，或主进程退出但仍有后代进程时，强制停止前的宽限期。默认 `5s`；接受 `0s` 到 `1m`。零表示跳过宽限期。 |
| `--mount type=bind,source=/HOST,target=/PATH[,readonly]` | 将 Linux 主机上已存在的目录绑定挂载到容器中。可重复指定，最多 32 个独立目标；添加 `readonly` 可禁止容器写入。 |

The `--user` flag selects the container identity; `--userns` and `--rootless` control host mappings. Copied files belong to container root. See [Security capabilities](README.md#security-capabilities) for mapping behavior and rootless supplementary groups. Templates must grant the selected identity access to the working directory and executables.

CPU 配额使用 cgroups v2 的 [`cpu.max`](https://www.kernel.org/doc/html/v6.8/admin-guide/cgroup-v2.html#cpu)。监督进程不受工作负载配额限制；init、命令进程及其线程共享该限制。

正常退出时透传命令退出码。因信号退出时使用 `128 + signal`；主命令仍在运行时发生超时会返回 `124`；已完成的命令在清理后代进程期间保留自己的退出码；配置错误、不支持的平台和启动错误返回 `125`。诊断信息写入 stderr。SIGINT 和 SIGTERM 会被转发，并使用有上限的停止宽限期。

## 本地镜像

目前尚未实现从镜像仓库下载镜像（`mdocker pull`）。可在 Linux 主机上导入准备好的 BusyBox 目录，然后使用存储的副本运行容器：

```sh
image_id=$(mdocker image import ./rootfs/busybox)
mdocker image ls
mdocker image ls --json
mdocker run --image "$image_id" -- /bin/sh -c 'echo image-run; hostname'
mdocker run -d --name image-worker --image "$image_id" -- /bin/sleep 300
mdocker inspect image-worker
mdocker exec image-worker -- /bin/echo shared-image
mdocker stop image-worker
mdocker rm image-worker
mdocker image rm "$image_id"
```

| 命令 | 行为 |
| --- | --- |
| `image import DIRECTORY` | 将稳定的 BusyBox 文件系统复制到本地镜像存储，并输出完整内容 ID。接受相对或绝对目录路径；拒绝解析路径组成部分中的符号链接、受保护的主机目录、特殊文件，以及已挂载的子目录或文件。 |
| `image ls [--json]` | 按 ID 排序列出镜像，包含架构、普通文件的逻辑字节数和 UTC 创建时间。JSON 返回数组，空存储返回 `[]`。 |
| `image rm ID` | 删除没有引用的镜像并输出其 ID。前台运行持有镜像租约，或任意后台容器记录引用它时，拒绝删除。镜像不存在时返回 `125`。 |
| `run --image ID ... -- COMMAND` | 从存储的镜像创建私有 rootfs 副本。`--image` 与 `--rootfs` 必须且只能选择一个；现有的资源、用户、环境变量、终端、挂载、超时和后台运行选项同样适用。 |

镜像 ID 由 `sha256:` 加上完整的 64 个小写十六进制字符组成。ID 的计算包含架构、排序后的路径、复制后的权限模式、普通文件内容和符号链接目标，不包含时间戳和所有权。与 rootfs 准备过程相同，导入会去掉 setuid/setgid 位，并将文件归属设为导入者。导入相同的复制内容会返回已有 ID 和创建时间；修改内容、权限模式、链接或架构都会改变 ID。此版本支持导入运行时所在主机原生 arm64 或 amd64 架构的本地目录；不支持标签、缩短的 ID、tar/归档导入、Docker/OCI 分层、镜像仓库访问或自动垃圾回收。

导入完成前，请保持源文件树不变。顶层源目录会被固定，源文件访问通过 `os.Root` 限制在指定范围内；文件符号链接会原样保留，而不会沿链接访问目标，包括 `/bin/busybox` 这样的容器内绝对链接。无效的 BusyBox 可执行文件，以及使用符号链接的运行时挂载目标，都会导致导入失败。主机根目录、`/proc`、`/sys`、`/dev`、运行时存储及其祖先目录均被拒绝。镜像发布是原子的；导入失败或取消时不会留下可见镜像。被中断的导入/删除操作留下的暂存目录，会在下一次导入或成功删除时回收，且不会跟随符号链接或跨越挂载点。

镜像存放于仅限私有访问、属于 root 的 `/var/lib/mini-docker/images/` 下。每个容器都有独立工作副本，因此修改容器或原始导入源不会改变镜像或其他容器。导入完成后可以删除源目录。创建容器前会根据 ID 校验存储内容，损坏的镜像会导致启动失败。请勿直接修改镜像存储。为保留引用跟踪机制，禁止通过原始 `--rootfs` 路径访问镜像存储。目录绑定挂载可为使用镜像的容器提供持久化的可写数据。

镜像操作需要 Linux 和 root 权限；导入和列出镜像不需要委派 cgroup 或 systemd。存储发布和删除使用独占锁，运行中的容器则各自持有独立的共享镜像租约。并发导入会去重；创建容器与删除镜像发生竞争时，要么保留受保护的镜像，要么以 `image not found` 失败。前台租约持续到运行时清理完成；监督进程突然消失会释放租约，因为工作负载使用的是独立 rootfs 副本。后台记录在退出、启动失败或监督进程恢复后仍保留引用，因此删除镜像前，必须停止并 `rm` 所有引用该镜像的容器。不提供强制删除选项。

对于使用镜像的容器，`inspect.config.image` 包含不可变 ID，`inspect.config.rootfs` 为空，以隐藏内部镜像存储路径。使用目录的容器则继续显示配置的 rootfs 路径，并省略 `image`。

## 持久化数据

在 Linux 主机上创建一个目录（使用 Lima 时，在 VM 内创建）：

```sh
mkdir -p "$HOME/mini-docker-data"
mdocker run --rootfs ./rootfs/busybox --read-only \
  --mount "type=bind,source=$HOME/mini-docker-data,target=/data" \
  --workdir /data -- /bin/sh -c 'echo persistent >> result; cat result'
mdocker run --rootfs ./rootfs/busybox \
  --mount "type=bind,source=$HOME/mini-docker-data,target=/data,readonly" \
  -- /bin/cat /data/result
```

`--mount` 可重复指定，只接受 `type=bind`、`source`、`target` 和可选的、不带值的 `readonly` 标志，默认为可写。选项顺序不固定；未知或重复选项会导致失败。路径必须是规范的绝对路径，不能包含 `.` 或 `..` 路径组成部分、末尾斜杠、NUL 或换行符；这种逗号分隔语法无法表示含逗号的路径。只支持已存在的真实源目录，路径中的任何组成部分都不能是符号链接。不会自动创建源目录。主机根目录、`/proc`、`/sys`、`/dev`、位于 `/var/lib/mini-docker` 的运行时存储，以及与 rootfs 模板重叠的源目录均被拒绝，其中也包括受保护路径的祖先目录。

目标不能是 `/`、`/tmp`，也不能与 `/proc`、`/dev` 或 `/sys` 重叠。允许 `/tmp/data`，它会在运行时的临时文件系统之后挂载。缺失的目标目录会在私有文件系统中以 `0755` 模式创建（受运行时 umask 影响）。已存在的目标目录在本次运行中会被覆盖；目标路径任意组成部分是文件或符号链接时，启动失败。无论选项顺序如何，都不允许重复或嵌套目标。挂载目录会隐藏目标之前的内容，但不会复制或删除它们。

绑定挂载不递归：源目录下已经挂载的子目录或文件不会被带入容器，容器看到的是被这些挂载覆盖的底层目录或文件。init 设置阶段会通过目录描述符固定源目录，因此后续重命名不会使已安装的挂载指向其他位置。挂载传播仅限容器内部。每个绑定挂载都强制启用 `nosuid` 和 `nodev`，保留源挂载的 `noexec` 和已有只读标志等限制，并且只将 `readonly` 应用于容器侧挂载。使用 `--read-only` 时，可写绑定挂载仍然可写；根文件系统可写时，只读绑定挂载仍然只读。工作负载被削减 capabilities 后无法重新挂载它。

主机文件所有权和权限会被保留；`--user` 直接使用这些主机上的数字 ID。运行前请授权所选用户访问源目录，并避免挂载敏感主机目录。可写绑定挂载允许工作负载修改或删除源数据。运行时不会在退出、超时、启动回滚、监督进程恢复或 `rm` 时修改源目录所有权、删除源目录或移除其内容。绑定挂载之外的文件系统写入仍是临时的。多个容器可以共享源目录，并遵循普通文件系统的并发规则。

相同选项也适用于后台容器。`exec` 继承已有挂载，不能添加新挂载。`inspect` 包含 `config.mounts`，它是由 `source`、`target` 和 `read_only` 对象组成的数组（未配置挂载时为空）；这些显式配置的主机路径在退出后仍然可见。

## 交互式终端

在 Linux VM 内的终端执行：

```sh
mdocker run -it --rootfs ./rootfs/busybox -- /bin/sh
mdocker run -it --rootfs ./rootfs/busybox \
  --user 1000:1000 --read-only --workdir /tmp -- /bin/sh
```

`-i` / `--interactive` 连接标准输入。普通前台运行仍默认转发标准输入；`--interactive=false` 则使用 `/dev/null`。`-t` / `--tty` 分配 PTY。可组合为 `-it`、`-ti` 或 `--interactive --tty` 来使用交互式终端。这些选项适用于前台执行；后台运行会拒绝已启用的交互或终端选项。

`-it` 要求标准输入来自终端，重定向或管道输入会返回 `125`。监督进程暂时将该终端设为原始模式，将输入复制到容器，并在退出、启动失败或处理终止信号后恢复原始设置。Ctrl+C 会中断容器当前的前台作业；交互式 shell 可以继续运行。命令启动前会复制窗口尺寸，并在收到 SIGWINCH 时更新。默认提供 `TERM=xterm`，除非通过 `--env TERM=...` 显式覆盖。

每次终端运行都会挂载私有的 [devpts 实例](https://www.kernel.org/doc/html/latest/filesystems/devpts.html)，最多 64 个 PTY，并提供 `/dev/pts`、`/dev/ptmx` 和 `/dev/tty`。命令在自己的会话中启动，拥有控制终端，支持 shell 作业控制。数字用户身份和只读根文件系统仍然受支持；终端挂载独立于根挂载，保持可写。主机 PTY 设备不会暴露给容器。

终端 stdout 和 stderr 合并输出到 stdout，并应用终端线路规程，例如 CRLF 换行。运行时诊断仍写入 stderr。退出后会在有上限的等待时间内排空输出，避免阻塞的消费者妨碍终端恢复。只指定 `-t` 而不指定 `-i` 时，会分配终端但不转发主机输入，并为规范模式下的读取程序排入 EOF 字符；如果标准输入没有窗口尺寸，初始大小为 24 行、80 列。正常命令退出码和超时保持原有行为。

## 后台容器

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --user 1000 --read-only --workdir /tmp \
  -- /bin/sh -c 'trap "echo stopped; exit 0" TERM; while :; do echo working; sleep 1; done'
mdocker ps
mdocker logs --tail 5 worker
mdocker stop --timeout 2s worker
mdocker wait worker
mdocker start worker
mdocker restart --timeout 2s worker
mdocker stop worker
mdocker ps --all --json
mdocker logs worker
mdocker rm worker
```

`run -d`（或 `--detach`）在命令启动后输出完整容器 ID，无需等待命令完成即可返回。后台标准输入为 `/dev/null`；stdout 和 stderr 合并写入私有日志。所有资源和执行选项同样适用于后台运行。启动最多等待 95 秒；失败时返回 `125`，如果已成功分配容器，则保留失败记录。即使命令立即退出，也会获得 ID 并保留退出码。

| 命令 | 行为 |
| --- | --- |
| `run -d --name NAME ...` | 预留唯一名称，长度为 1–63 个字母、数字、点、下划线或连字符，且以字母或数字开头。名称不能是完整容器 ID。省略名称时自动生成 `mini-...` 名称。 |
| `ps` | 列出已创建、正在启动、运行中和正在停止的容器。`-a` / `--all` 包含已退出和失败记录；`--json` 返回适合脚本处理的数组。 |
| `inspect ID\|NAME` | 以 JSON 对象输出公开配置、生命周期状态、时间戳、退出状态和资源限制。 |
| `stats ID\|NAME` | 输出一次实时内存和 CPU 采样；`--json` 返回对象，`--interval DURATION` 设置 CPU 采样窗口。 |
| `wait ID\|NAME` | 等待指定的一次执行，输出记录的退出码，并以该退出码作为 CLI 退出状态。可以对已完成容器重复调用，不改变其状态。状态未知时返回 `125`，输出诊断信息，不输出 stdout。 |
| `start ID\|NAME` | 使用相同 ID、名称、配置和保留的 rootfs，启动已退出或失败的容器。对已经运行的容器调用会成功返回，不改变当前执行。 |
| `restart [--timeout DURATION] ID\|NAME` | 停止选定的执行，然后使用保留的 rootfs 启动新一次执行。已完成的容器也可以重启。 |
| `stop [--timeout DURATION] ID\|NAME` | 通过监督进程发送 SIGTERM，并等待清理。使用保存的 `--stop-timeout`，或通过 `--timeout`（`0s` 到 `1m`）仅覆盖此次操作。对已完成容器重复停止会成功返回。 |
| `logs ID\|NAME` | 读取保留的合并输出。`--tail N` 选择最后 N 行；`-f` / `--follow` 持续输出直到选定的执行结束，即使同时发生重启也遵循此规则。中断日志跟随不会停止容器。 |
| `rm ID\|NAME` | 确认资源已清理后，删除非活动容器保留的 rootfs、元数据、配置、执行结果记录和日志。活动容器必须先停止。删除后名称可再次使用。 |

使用完整的 32 字符 ID 或精确名称。选项必须位于标识符之前，例如 `logs --tail 10 --follow worker`。除 `wait` 返回记录的工作负载退出状态外，管理命令成功时返回 `0`；错误时返回 `125`。也可以通过 `ps --all` 和 `inspect` 查看工作负载退出状态。中断 `wait` 时，SIGINT 返回 `130`，SIGTERM 返回 `143`，工作负载仍继续运行。

每次执行都有独立的临时 systemd 服务，使用 [cgroup 委派](https://systemd.io/CGROUP_DELEGATION/)。关闭启动器不会停止它。持久记录、执行结果记录、日志和私有 rootfs 副本存放在 `/var/lib/mini-docker/containers/` 下，其父目录属于 root 且仅限私有访问。临时运行状态存放在 `/var/lib/mini-docker/runs/` 下，并在执行结束后删除。

`start` 和 `restart` 保留私有 rootfs 中的文件系统修改、所有权和权限模式，也保留绑定挂载源中的数据。它们会创建新的命名空间、cgroup、`/proc`、`/dev` 和 `/tmp`；`/tmp` 下的临时文件不会保留。保留的副本不再依赖原始 rootfs 模板。绑定挂载源必须仍然存在，镜像引用会保留到 `rm` 为止。在实现 rootfs 保留之前创建的旧容器，如果原始模板或镜像仍然存在，也可以启动，但之前已经丢弃的文件系统修改无法恢复。启动失败后可以重试；启动准备会原子发布完整 rootfs，并丢弃不完整副本。

执行编号从 `generation: 0` 开始。启动已停止的容器会增加 generation，重置当前执行的时间戳和退出码，并在检查结果的 `previous_exit` 中显示上一次执行结果。每次已完成执行的结果记录都会保留到容器删除。`wait` 在调用时固定 ID 和 generation，因此并发重启不会替换它正在等待的退出结果。管理操作对每个容器串行执行；如果排队的操作所针对的执行已经变化，则返回 `125`，可以重试。删除容器或复用名称不会让尚未完成的操作转向另一个容器。

运行时会执行选定的停止宽限期，随后杀死剩余工作负载进程，并在有限时间内完成清理。systemd 提供 75 秒的最终服务终止边界：如果监督进程无法完成停止，这段时间涵盖最长一分钟的宽限期及清理。管理停止操作的截止时间为所选宽限期再加 25 秒，用于状态核对和清理。

日志在多次执行之间追加，最多保留合并输出的前 16 MiB，必要时包含截断提示。超出部分会被持续读取并丢弃，避免日志写满后阻塞工作负载；JSON 记录提供 `log_truncated`。此版本不轮转日志，也不会在主机/VM 重启后自动重启容器；完成状态核对后可使用 `start`。管理命令通过服务身份和锁核对丢失的监督进程，保留失败记录；如果监督进程突然消失导致完成状态无法记录，命令退出状态会保持未知。

## 检查与资源统计

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --memory 64m --cpus 0.5 --env MODE=demo -- /bin/sleep 300
mdocker inspect worker
mdocker stats worker
mdocker stats --json --interval 500ms worker
mdocker stop worker
mdocker inspect worker
mdocker stats --json worker
mdocker rm worker
```

这些命令接受后台容器的完整 ID 或精确名称，包括已完成和失败的容器，需要 Linux 和 root 权限。`inspect` 始终输出 JSON 对象。其 `config` 包含 rootfs 模板路径或存储的镜像 ID、主机名、命令参数、实际使用的工作目录和数字用户身份、只读和终端设置、目录绑定挂载，以及用时长字符串表示的执行超时和停止宽限期。`environment_names` 列出实际生效的环境变量名称（包含默认值），但不显示变量值。检查结果不包含环境变量值、原始运行时错误、系统启动标识、临时文件系统路径或 cgroup 路径。命令参数和配置的 rootfs 路径会有意保留可见，命令参数本身也已经显示在 `ps` 中。

`created_at`、`started_at` 和 `finished_at` 使用 UTC RFC3339 时间戳。尚未发生的时间和未知退出码在 JSON 中为 `null`。`limits` 包含配置的 `memory_bytes`、`pids`、`cpu_quota_usec`、`cpu_period_usec` 和 `cpus`；CPU 配额为零且 `cpus` 为零时表示不限制。资源清理后仍可查看限制和配置。`generation`、`previous_exit` 和 `filesystem_retained` 描述重启历史和 rootfs 保留策略。

`stats` 返回单次采样后退出。它采样工作负载的 cgroups v2 [`memory.current` 和 `cpu.stat`](https://www.kernel.org/doc/html/v6.8/admin-guide/cgroup-v2.html)，包括 exec 后代进程，不包括监督进程。内存使用量包含计入 cgroup 的页缓存和内核内存，而不只是进程 RSS。CPU 使用率为两次读取之间的 `usage_usec` 差值，除以单调时钟测得的实际间隔。一个完全占用的 CPU 核心对应 `100%`；使用多个核心时可以超过 `100%`。结果不会按主机 CPU 数量或配置配额归一化。默认采样间隔为一秒；`--interval` 接受 `10ms` 到 `1m`。受到 CPU 节流时，较短的间隔可能产生较大波动。

表格以字节显示内存使用量和配置上限，并显示 CPU 百分比。JSON 包含 `memory_bytes`、`memory_limit_bytes`、`cpu_percent`、累计 `cpu_usage_usec`、UTC `sampled_at`，以及用时长字符串表示的实际 `interval`。缺失或无效计数器分别处理：可用指标仍然显示，不可用指标在 JSON 中为 `null`，在表格中为 `N/A`，JSON 还提供 `memory_unavailable` / `cpu_unavailable` 原因。空闲工作负载可以报告有效的 `0%`；不可用指标不会被表示为零使用量。已完成的容器没有实时指标，也没有保留的历史使用量，因此立即返回 `interval: "0s"`。

状态和配置在共享存储锁下同时读取。采样会固定一个 cgroup 目录，不跟随符号链接，也不会在等待时持有存储锁。如果选定的执行在采样期间完成或重启，实时指标会返回不可用。并发删除时，要么获得完整快照，要么返回 `125` 和 `container not found`；名称复用不会使正在进行的请求转向另一个容器。中断统计请求会返回相应信号的退出状态，不会停止工作负载。

## 在运行中的容器内执行命令

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --env MODE=base --workdir /tmp -- /bin/sleep 300
mdocker exec worker -- /bin/sh -c 'hostname; pwd; echo "$MODE"'
mdocker exec --env MODE=check --workdir / --timeout 10s \
  worker -- /bin/sh -c 'pwd; echo "$MODE"'
printf 'hello\n' | mdocker exec -i worker -- /bin/cat
mdocker stop worker
mdocker rm worker
```

`exec ID|NAME -- COMMAND [ARGS...]` 要求目标是运行中的后台容器，具备 root 权限，并且内核支持带 `CLONE_INTO_CGROUP` 的 `clone3`。使用完整 ID 或精确名称，并将选项放在标识符之前。前台运行、非活动容器或正在停止的容器不接受执行请求。

每个命令共享容器的 PID、挂载、UTS、IPC 和网络命名空间，以及当前文件系统，包括其他进程写入的内容。命令继承容器配置的环境变量、工作目录、数字 UID/GID、capability 限制和只读挂载。可重复的 `--env KEY=VALUE` 和 `--workdir /PATH` 只覆盖本次调用的默认值；不会继承宿主环境变量。不能通过 `exec` 覆盖用户身份。

标准输入默认为 `/dev/null`；`-i` / `--interactive` 转发调用方输入。不指定 `-t` 时，stdout 和 stderr 分别输出给调用方。exec 输出不会添加到后台日志。正常退出码透传，信号退出返回 `128 + signal`，执行错误返回 `125`，`--timeout D` 在到达截止时间后返回 `124`。超时从命名空间辅助进程启动时开始计时，包含命令启动过程；`0` 表示不限制。

所有命令共享容器的总内存、进程/线程和 CPU 限制。每次调用使用一个子 cgroup 完成清理，包括创建了新会话的后代进程。客户端断开、处理终止信号和超时只会停止本次调用，主容器继续运行。容器停止或主命令退出会取消所有活动调用。终止时允许五秒宽限期，随后强制清理。监督进程最多接受 16 个并发会话；请求和配置载荷上限为 64 KiB。在运行中的容器整个生命周期内，命名空间、根目录和可执行文件描述符都会保持固定。

### 交互式 exec 终端

在 Linux VM 内的终端启动后台容器，然后打开 shell：

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --user 1000:1000 --read-only --workdir /tmp -- /bin/sleep 300
mdocker exec -it worker -- /bin/sh
# Exit the shell with exit or Ctrl+D; worker keeps running.
mdocker stop worker
mdocker rm worker
```

使用 `exit` 或 Ctrl+D 退出 shell 后，worker 仍继续运行。`-t` / `--tty` 分配终端；结合 `-i`，写为 `-it`、`-ti` 或 `--interactive --tty`，可以连接输入。交互式终端执行要求标准输入来自终端，管道输入会在创建会话前被拒绝并返回 `125`。只指定 `-t` 时不转发输入，并为规范模式下的读取程序排入 EOF；如果调用方标准输入没有终端尺寸，初始大小默认为 24 行、80 列。

每个后台容器在启动时挂载私有 devpts 实例，最多 64 个 PTY。exec 会话在该实例中分配独立 PTY，不改变主进程输入，也不重新挂载文件系统。支持数字用户身份和只读根文件系统。升级运行时后，请启动新容器以启用终端执行；旧的运行中监督进程无法增加此支持。

客户端复用前台终端处理机制：原始输入、命令启动前设置初始窗口尺寸、SIGWINCH 尺寸更新、有限等待时间内排空输出，以及在正常退出、启动失败、超时、处理终止信号或容器停止时恢复终端。支持 shell 作业控制。Ctrl+C 中断终端前台作业，因此交互式 shell 可以继续运行。独立 exec 会话可以并发执行；退出或取消一个会话不会影响主容器或其他会话。

终端 stdout 和 stderr 合并到 stdout，并应用终端换行处理。运行时诊断仍写入 stderr。`TERM=xterm` 为默认值，除非容器环境或 exec 的 `--env TERM=...` 覆盖它。环境变量、用户身份、工作目录、资源限制、超时和后代进程清理规则与普通 exec 相同。

## 构建与验证

| 命令 | 用途 |
| --- | --- |
| `make build` | 构建静态 Linux 二进制文件 `bin/mdocker`，同时创建兼容符号链接 `bin/mini-docker` |
| `sudo make install` | 将已构建的 Linux CLI 安装到 `/usr/local/bin/mdocker` |
| `make rootfs` | 使用已安装的静态 BusyBox 生成 `rootfs/busybox` |
| `make fmt` | 格式化 Go 源码 |
| `make vet` | 运行 Go 静态检查 |
| `make test` | 运行非特权测试；跳过集成测试 |
| `make test-integration` | 在专用 scope 和服务中运行特权 Linux 集成测试 |

`make build GOARCH=amd64` 可以交叉编译 amd64。无论继承的 `GOOS` 或 `GOARCH` 如何设置，`make test-integration` 都会为 Linux VM 的原生架构构建；测试启动器会检查运行时的 ELF 架构。单元测试和 vet 也可以在 macOS 上运行。容器执行和 rootfs 准备需要 Linux。`make rootfs` 拒绝覆盖已有目标；重新生成前需要显式删除。生成的 `.mini-docker-rootfs.json` 记录架构、软件包版本和 SHA-256 校验和。

集成测试要求专用 VM，缺少前置条件时会失败。它们覆盖执行、输入/输出、退出状态、隔离、权限、资源限制、信号处理、超时、子进程清理、重复运行和并发。额外测试验证实际 CPU 节流、环境变量和命令查找、工作目录错误、非 root 身份及清理，以及只读根文件系统搭配可写临时存储。

后台测试覆盖独立生命周期、保留的状态和日志、日志尾部读取/跟随/取消、日志上限、名称、并发管理、有上限的停止、删除，以及监督进程丢失后的恢复。终端测试验证交互式 shell 输入、作业控制、Ctrl+C、窗口尺寸更新、私有 PTY、主机终端设置恢复、输入模式和输出排空。使用 `./scripts/test-linux.sh -test.run TestTerminal` 单独运行终端测试，或使用 `-test.run TestBackground` 运行后台管理测试。

exec 测试覆盖共享命名空间和文件系统、继承配置与身份、流分离、实际退出码、共享 CPU 限制、并发会话、取消、后代进程清理、启动器移除和容器停止。交互式 exec 测试还覆盖作业控制、窗口尺寸更新、终端恢复、独立 PTY 和会话清理。使用 `./scripts/test-linux.sh -test.run TestExecTerminal` 运行终端 exec 检查，或使用 `-test.run TestExec` 运行全部 exec 检查。

检查和统计测试覆盖活动、已完成和失败记录、配置隐私、实际 CPU 和内存统计、空闲工作负载、不可用指标、采样取消，以及并发退出、删除和名称复用。使用 `./scripts/test-linux.sh -test.run "TestInspection|TestStats"` 运行这些检查。

绑定挂载测试验证跨运行持久化、只读限制、主机所有权、只读根文件系统上的可写数据、私有挂载传播、并发容器和 exec、排除子挂载、继承源挂载限制、无效路径、部分启动回滚、超时、监督进程恢复，以及删除容器后保留数据。使用 `./scripts/test-linux.sh -test.run TestBindMount` 运行这些检查。

镜像测试覆盖导入身份和去重、源目录独立性、隔离的私有副本、绑定挂载和数字用户身份、保留的引用和监督进程恢复、前台租约、并发创建/删除、不安全源、特殊文件、已挂载子树和部分导入清理。镜像存储单元测试还验证取消、损坏的内容和元数据、并发导入/读取，以及暂存目录恢复。使用 `./scripts/test-linux.sh -test.run TestImage` 运行镜像检查。

启动器测试覆盖自动创建 scope、管道输入、参数保留、退出码、超时、运行能力检查，以及委派失败时防止递归启动。使用 `./scripts/test-linux.sh -test.run TestLaunch` 运行这些检查。

生命周期测试覆盖 wait 状态和取消、保留的 rootfs 与绑定挂载数据、每次执行使用新的临时存储、重启前后的执行结果记录、启动失败重试、停止参数覆盖和截止时间、并发启动、不安全的保留挂载，以及累计日志上限。使用 `./scripts/test-linux.sh -test.run TestLifecycle` 运行这些检查。资源测试使用有边界的辅助程序和截止时间。

## 后续里程碑

对外联网和安全能力仍在规划中。生命周期扩展已经实现，包括保留文件系统和按执行记录退出结果。每项新增功能都必须保持前台与后台执行的隔离和清理保证，并通过特权 Linux 集成测试。

## 源码结构

- `cmd/mini-docker/`：可执行程序入口。
- `internal/cli/`：参数解析和环境检查。
- `internal/config/`：执行配置与校验。
- `internal/container/`：持久记录、后台监督、systemd 服务管理和有上限的日志。
- `internal/runtime/`：监督进程、容器 init、信号、运行状态、恢复和清理。
- `internal/rootfs/`：模板校验和限制访问范围的文件系统准备。
- `internal/image/`：内容寻址的本地镜像、导入发布、完整性检查和删除租约。
- `internal/cgroup/`：cgroups v2 委派与限制。
- `internal/ipc/`：大小受限的本地消息，以及设置 close-on-exec 的描述符传递。
- `scripts/`、`dev/`：Linux 启动器和开发 VM 配置。
- `tests/integration/`：特权 Linux 行为测试。

生成的二进制文件、rootfs 模板和 `docs/` 不纳入 Git。本文件提供 README 的简体中文版；其他项目文档、代码、注释、诊断信息和提交消息使用英语。

## Security capabilities update

Seccomp filtering is now enabled by default for run and exec; `--seccomp unconfined` disables the filter while retaining capability restrictions. Foreground runs support explicit `--userns --uid-map C:H:N --gid-map C:H:N` mappings. `--rootless` maps container `0:0` to the caller and uses a delegated systemd user scope without sudo, with enforced memory, PID, and CPU limits. User namespace modes currently require foreground, loopback-only execution; rootless mode also requires a directory template and a single caller mapping. See [Security capabilities](README.md#security-capabilities) for complete usage, prerequisites, AppArmor configuration, supplementary-group behavior, and limitations.
