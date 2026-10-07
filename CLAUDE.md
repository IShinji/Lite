# 本仓库是带自定义补丁的 fork（先读这里）

这不是上游原版。`IShinji/Lite` 是 `nuomiiiii/Lite` 的 fork，上面叠了几层**本地补丁**，上游维护者不接受这些改动，所以由本 fork 自己维护。
**任何同步上游、改补丁、构建发布的工作，都先按本文件的流程来。**

## 补丁清单（共 3 层，按顺序叠加）

| 分支 | 作用 | 主要文件 |
| --- | --- | --- |
| `local-patches/browser-tz` | 仪表盘流量按访问者浏览器时区统计（含历史 30 天）。新增 15 分钟粒度账本 `traffic_bucket_ledgers`；四个仪表盘接口加可选 `tz`（query 或 RPC params），缺省/Asia/Shanghai 走原逻辑；历史回填不足的天标 `partial`，不与新数据拼接 | 新增：`database/trafficledger/bucket_ledger.go`、`tz.go`、`database/models/traffic_bucket_ledger.go`、`web/rpc/jsonrpc/admin.dashboard_tz.go`；小改：`admin.dashboard.go`、`admin.dashboard_traffic_day.go`、`admin.dashboard_cache.go`、`web/router/router.go`、`trafficledger.go`（`Maintain` 末尾一行）、`database/dbcore/dbcore.go`、`database/clients/client.go` |
| `local-patches/no-self-update` | 禁用后台“立即更新”。原因：官方更新从上游 Release 下载二进制，会覆盖补丁。`DetectCapability` 恒返回 `Supported=false, Reason=local_patch`，服务端更新入口同样被拒；前端据此显示“请自行同步上游并重新构建” | 新增：`pkg/selfupdate/local_patch.go`；小改：`pkg/selfupdate/deployment.go`（`DetectCapability` 开头 4 行） |
| `local-patches/ci-skip-stale-tests` | `snapshot.yml` 的 `go test` 跳过 3 个上游本身就失败的测试（含写死日期的用例）：`TestGetPingRecordsKeepsRequestedWindowInsideLongerRetention`、`TestRecoveryHoldLearnsOnlyAfterFullCleanWindow`、`TestThemeNodeOmitsForbiddenFieldsForGuestAndAdmin`。上游修好后应去掉 | `.github/workflows/snapshot.yml` 一行 |

不要恢复“立即更新”。不要把计费、到期、周期重置、通知、`scheduledexec` 里的北京时间逻辑改成跟随浏览器时区，它们有意保持 Asia/Shanghai。

## 仓库与 remote

| 本地目录 | origin（我的 fork） | upstream（原仓库） | 补丁分支栈 |
| --- | --- | --- | --- |
| `/Users/wisely/Documents/GitHub/Lite`（后端，本仓库） | `IShinji/Lite` | `nuomiiiii/Lite` | `main` → `browser-tz` → `no-self-update` → `ci-skip-stale-tests` |
| `/Users/wisely/Documents/GitHub/Lite-web`（管理端前端） | `IShinji/Lite-web` | `nuomiiiii/Lite-web` | `main` → `browser-tz` → `no-self-update`，另有自己的 `CLAUDE.md` |
| 无本地目录（公开主题，无补丁） | `IShinji/Lite-theme` | `nuomiiiii/Lite-theme` | 只需保持 `main` 与上游一致 |

前端的补丁：仪表盘请求带浏览器时区 `tz`、缓存键按时区隔离、日期标签不再按 `+08:00` 解析、不完整天提示；`local_patch` 时隐藏“立即更新”。

## 构建与发布机制

- 每个 fork 的 `Snapshot` 分支 = 该仓库**最上层补丁分支**。推送 `Snapshot` 会触发 `snapshot.yml`：跑后端测试、编译 5 种架构、把镜像发布到 `ghcr.io/ishinji/lite`、创建 `Snapshot-<时间戳>` 预发布。
- Lite 的构建（`.github/actions/build-frontend`）会拉**我 fork 名下**的 `Lite-web`（分支 `Snapshot`）和 `Lite-theme`（分支 `main`）。所以推送顺序必须是：**Lite-theme 同步 → Lite-web 推 Snapshot → Lite 推 Snapshot**。
- 新 fork 的 Actions 需要在网页上手动点一次启用，已经启用过，无需再做。

## 上游更新后的同步流程

1. **影响评估**：对三个仓库 `git fetch upstream`，`git log` 看新增提交。重点看上游是否改动了：`admin.dashboard*.go`、`web/router/router.go`、`trafficledger`、`pkg/selfupdate/deployment.go`、`snapshot.yml`；前端的 `dashboardApi.ts`、`dashboard.ts`、`UpdateReleaseDialog.tsx`、`adminShellModel.ts`。先给用户一份影响评估再动手。
2. **同步 main**：fork 的 `main` 对 `upstream/main` 做 `merge --ff-only`；`Lite-theme` 用 `gh repo sync IShinji/Lite-theme`。
3. **逐层 rebase**：先 `browser-tz` 到新 `main`，再 `no-self-update` 到 `browser-tz`，再 `ci-skip-stale-tests`（后端）；前端同理两层。冲突逐个解决并说明。
4. **复测**：
   - 后端：`go build ./... && go vet ./database/trafficledger/... ./web/rpc/jsonrpc/... ./pkg/selfupdate/...`；`go test ./database/trafficledger/... ./pkg/selfupdate/...`；`go test ./web/rpc/jsonrpc -run 'Tz|Update|SelfUpdate|Dashboard'`。全量 `go test ./...` 要带上本文件所列的 `-skip`。
   - 前端：`npm ci`、`npx tsc -b`、`node --test tests/dashboard.test.ts tests/adminShellModel.test.ts`。
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
