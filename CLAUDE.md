# 本仓库是带自定义补丁的 fork（先读这里）

这不是上游原版。`IShinji/Lite` 是 `nuomiiiii/Lite` 的 fork，上面叠了几层**本地补丁**，上游维护者不接受这些改动，所以由本 fork 自己维护。
**任何同步上游、改补丁、构建发布的工作，都先按本文件的流程来。**

## 补丁清单（共 5 层，按顺序叠加；Lite-theme 另有 1 个补丁分支）

| 分支 | 作用 | 主要文件 |
| --- | --- | --- |
| `local-patches/browser-tz` | 仪表盘流量按访问者浏览器时区统计（含历史 30 天）。新增 15 分钟粒度账本 `traffic_bucket_ledgers`；四个仪表盘接口加可选 `tz`（query 或 RPC params），缺省/Asia/Shanghai 走原逻辑；历史回填不足的天标 `partial`，不与新数据拼接 | 新增：`database/trafficledger/bucket_ledger.go`、`tz.go`、`database/models/traffic_bucket_ledger.go`、`web/rpc/jsonrpc/admin.dashboard_tz.go`；小改：`admin.dashboard.go`、`admin.dashboard_traffic_day.go`、`admin.dashboard_cache.go`、`web/router/router.go`、`trafficledger.go`（`Maintain` 末尾一行）、`database/dbcore/dbcore.go`、`database/clients/client.go` |
| `local-patches/no-self-update` | 禁用后台“立即更新”。原因：官方更新从上游 Release 下载二进制，会覆盖补丁。`DetectCapability` 恒返回 `Supported=false, Reason=local_patch`，服务端更新入口同样被拒；前端据此显示“请自行同步上游并重新构建” | 新增：`pkg/selfupdate/local_patch.go`；小改：`pkg/selfupdate/deployment.go`（`DetectCapability` 开头 4 行） |
| `local-patches/ci-skip-stale-tests` | `snapshot.yml` 的 `go test` 跳过 3 个上游本身就失败的测试（含写死日期的用例）：`TestGetPingRecordsKeepsRequestedWindowInsideLongerRetention`、`TestRecoveryHoldLearnsOnlyAfterFullCleanWindow`、`TestThemeNodeOmitsForbiddenFieldsForGuestAndAdmin`。上游修好后应去掉 | `.github/workflows/snapshot.yml` 一行 |
| `local-patches/ping-stats-fix` | 修 `public:getPingMetricStats`：100% 丢包不再返回 `0` 延迟（节点卡片不再显示 0 ms），丢包时 `avg/p50/p99/stddev/min/max/latest` 按成功样本加权，不再被失败探测拉低。**这是 3 个 cherry-pick 的提交，内容与已提交给上游的 PR 相同：https://github.com/nuomiiiii/Lite/pull/7，上游合并后这一层应整层删除** | `web/rpc/jsonrpc/public.metric.go`、`web/rpc/jsonrpc/public_metric_all_lost_test.go` |
| `local-patches/theme-ref-ping-chart-gaps` | `snapshot.yml` 里 `theme-ref` 从 `main` 改为 `local-patches/ping-chart-gaps`，让构建使用 Lite-theme 的补丁分支（图表区分丢包与无记录，见下表）。**对应上游 PR：https://github.com/nuomiiiii/Lite-theme/pull/1，上游合并后把这一行改回 `main` 并删掉这一层** | `.github/workflows/snapshot.yml` 一行 |

不要恢复“立即更新”。不要把计费、到期、周期重置、通知、`scheduledexec` 里的北京时间逻辑改成跟随浏览器时区，它们有意保持 Asia/Shanghai。

## 仓库与 remote

| 本地目录 | origin（我的 fork） | upstream（原仓库） | 补丁分支栈 |
| --- | --- | --- | --- |
| `/Users/wisely/Documents/GitHub/Lite`（后端，本仓库） | `IShinji/Lite` | `nuomiiiii/Lite` | `main` → `browser-tz` → `no-self-update` → `ci-skip-stale-tests` |
| `/Users/wisely/Documents/GitHub/Lite-web`（管理端前端） | `IShinji/Lite-web` | `nuomiiiii/Lite-web` | `main` → `browser-tz` → `no-self-update`，另有自己的 `CLAUDE.md` |
| 无本地目录（公开主题，**有 1 个补丁分支**） | `IShinji/Lite-theme` | `nuomiiiii/Lite-theme` | `main` 保持与上游一致；补丁在 `local-patches/ping-chart-gaps` |

前端的补丁：仪表盘请求带浏览器时区 `tz`、缓存键按时区隔离、日期标签不再按 `+08:00` 解析、不完整天提示；`local_patch` 时隐藏“立即更新”。

## Lite-theme 补丁分支（`local-patches/ping-chart-gaps`）

- 内容：`feat/ping-chart-gaps`（即上游 PR 的那个中文提交，图表区分丢包与无记录、新增「采样完整度」列）+ 1 个自己的提交（把主题版本号提到 `1.2.6`）。
- **为什么要提版本号**：Lite 启动时只在内置主题版本号**数字更大**时才覆盖数据目录里已安装的主题，版本号后缀（如 `-ping1`、`.1`）会被忽略。补丁主题若仍是 `1.2.5`，已安装 `1.2.5` 的实例看不到改动。版本号要在 `Lite-theme.json`、`package.json`（及 `package-lock.json` 根包）、`tests/theme-settings.test.ts` 里写死的断言三处**保持一致**。
- **同步上游主题时**：先把 fork 的 `main` 快进，再 rebase 补丁分支。版本号那个提交几乎一定会冲突：补丁版本号必须**大于上游当前版本**（取「上游版本 + 1」），否则不会替换已安装的主题。
- 上游 PR #1 合并后：删掉这个补丁分支、把 `snapshot.yml` 的 `theme-ref` 改回 `main`、删掉 `theme-ref-ping-chart-gaps` 层。已安装的补丁版 `1.2.6` 要等上游主题版本号超过 `1.2.6` 才会被替换，在那之前可在管理端手动重新安装主题。

## 构建与发布机制

- 每个 fork 的 `Snapshot` 分支 = 该仓库**最上层补丁分支**。推送 `Snapshot` 会触发 `snapshot.yml`：跑后端测试、编译 5 种架构、把镜像发布到 `ghcr.io/ishinji/lite`、创建 `Snapshot-<时间戳>` 预发布。
- Lite 的构建（`.github/actions/build-frontend`）会拉**我 fork 名下**的 `Lite-web`（分支 `Snapshot`）和 `Lite-theme`（分支由 `snapshot.yml` 的 `theme-ref` 决定，**现在是 `local-patches/ping-chart-gaps`**，不是 `main`）。所以推送顺序必须是：**Lite-theme 同步并推补丁分支 → Lite-web 推 Snapshot → Lite 推 Snapshot**。
- 新 fork 的 Actions 需要在网页上手动点一次启用，已经启用过，无需再做。

## 上游更新后的同步流程

1. **影响评估**：对三个仓库 `git fetch upstream`，`git log` 看新增提交。重点看上游是否改动了：`admin.dashboard*.go`、`web/router/router.go`、`trafficledger`、`pkg/selfupdate/deployment.go`、`snapshot.yml`；前端的 `dashboardApi.ts`、`dashboard.ts`、`UpdateReleaseDialog.tsx`、`adminShellModel.ts`。同时查两个上游 PR 的状态（`gh pr view 7 --repo nuomiiiii/Lite`、`gh pr view 1 --repo nuomiiiii/Lite-theme`）：**已合并或被上游以别的方式修复的，对应的本地补丁层要整层删掉**（`ping-stats-fix`、`theme-ref-ping-chart-gaps` 与 Lite-theme 的 `local-patches/ping-chart-gaps`），不要带着重复的修复去 rebase。先给用户一份影响评估再动手。
2. **同步 main**：fork 的 `main` 对 `upstream/main` 做 `merge --ff-only`；`Lite-theme` 用 `gh repo sync IShinji/Lite-theme`（fork 的 `main` 要保持等于上游，补丁只在补丁分支上）。
3. **逐层 rebase**：后端依次 `browser-tz` → `no-self-update` → `ci-skip-stale-tests` → `ping-stats-fix` → `theme-ref-ping-chart-gaps`，每一层 rebase 到前一层；前端同理两层；Lite-theme 的 `local-patches/ping-chart-gaps` rebase 到新的 `main`，**版本号提交按上面的规则取「上游版本 + 1」**。冲突逐个解决并说明。
4. **复测**：
   - 后端：`go build ./... && go vet ./database/trafficledger/... ./web/rpc/jsonrpc/... ./pkg/selfupdate/...`；`go test ./database/trafficledger/... ./pkg/selfupdate/...`；`go test ./web/rpc/jsonrpc -run 'Tz|Update|SelfUpdate|Dashboard|PublicPing|PingMetric'`。全量 `go test ./...` 要带上本文件所列的 `-skip`。
   - 前端（Lite-web）：`npm ci`、`npx tsc -b`、`node --test tests/dashboard.test.ts tests/adminShellModel.test.ts`。
   - 主题（Lite-theme 补丁分支）：`npm ci`、`npx tsc -b`、`npm test`、`npm run build`（CI 会跑 build，不能只看测试）。
5. **检查跳过的测试**：上游若已修好那 3 个测试，去掉 `snapshot.yml` 里的 `-skip`。
6. **扫描**：确认 diff 里没有真实 IP、令牌、UUID（示例用 `example.org`、`203.0.113.x`）。
7. **推送**（由用户执行，见下）。推完用 `gh run list --repo IShinji/Lite` / `gh run view` 盯构建，成功后告知用户 Release 标签与镜像地址。

## 约束（AI 必须遵守）

- **不要自己 push**。推送命令按顺序列成“每条一行、不要换行”的 `! ` 命令交给用户执行（自动模式会拦截向 `Snapshot` 的推送，命令换行也会断开）。前端 `Snapshot` 若需覆盖用 `git push -f origin <分支>:Snapshot`，只覆盖我们自己 fork 的 `Snapshot`，不要对上游或 `main` 强推。
- 不 ssh、不碰线上服务器、不读用户凭据。部署由用户自己做。
- 升级前提醒用户**备份 `data` 目录**（补丁带数据库迁移 `traffic_bucket_ledgers`）。
- 新账本只从部署后开始累积，加上回填 metricstore 仍保留的数据；更早的历史天数显示“数据不完整”是预期行为，不是 bug。
- 补丁尽量放在新增文件里，对上游文件只做最小改动，方便 rebase。
- 会改代码的子 agent 一律用 worktree 隔离；报告要说明哪些部分没验证。

## 已知的未验证项

- 时区统计没有对着真实 metricstore、Postgres/MySQL 联调过，只用 SQLite 和模拟数据测过。
- 前端的“数据不完整”tooltip 和“立即更新”警告的实际样式没有在浏览器里看过。
- `getDashboard` 里“今日恢复”告警计数仍按北京日算，只回显 `tz`。
