# 部署代理 使用说明

## 运行

```bash
# Windows 安装 Go 1.21+ 后，在项目根目录执行
cd deployagent
# 初始化依赖
go mod tidy
# 运行
go run .
```

服务默认监听 `:8080`。首次请修改 `config.yaml` 中的 `secret`、`users`。

## 目录与配置
- `config.yaml`：服务端口、密钥、上传/脚本/日志目录、登录用户
- `web/`：简单前端（登录、部署、日志、Docker）
- `scripts/`：放置 `{code}.sh` 脚本。例如 `example.sh`
- `uploads/`：上传文件保存位置
- `logs/`：日志目录（供查看/追踪）

## 部署流程
1. 登录：访问 `http://localhost:8080`，使用配置的用户名密码
2. 在“部署”区填写 `code`（仅字母数字-_）和 `secret`（示例用，生产应由后端生成签名或自有签名工具）`
3. 选择文件并提交
4. 服务端校验签名 `md5(secret+code+timestamp)`，保存文件，执行 `scripts/{code}.sh` 将文件路径作为参数传入，同时也通过环境变量 `UPLOAD_FILE`、`CODE` 传入

## 日志
- 列表：`/api/logs`
- 查看：`/api/logs/view?file={name}&start={0基行}&n={行数}`
- 追踪：`/api/logs/tail?file={name}`（SSE）

## Docker
- 列出容器：`/api/docker/ps`
- 查看容器日志：`/api/docker/logs?id={容器ID或名称}`

## 安全建议
- 必须修改 `config.yaml` 的 `secret` 和账户
- 根据需要限制脚本目录权限，并审计脚本内容
- 建议在内网或加上反向代理与 TLS

## Jenkins - Execute NodeJS script 调用 /deploy 示例
在 Jenkins 任务中添加“Execute NodeJS script”步骤，Node 版本建议使用 18+（内置 `fetch`/`FormData`/`Blob`）。在构建前配置以下环境变量（可使用 Credentials 注入）：
- `DEPLOY_URL`：例如 `http://your-host:8080/deploy`
- `DEPLOY_SECRET`：与服务端 `config.yaml.server.secret` 一致
- `DEPLOY_CODE`：部署 code（仅字母、数字、-_）
- `DEPLOY_FILE`：要上传的文件绝对路径（可选）

将下面脚本粘贴到“Execute NodeJS script”中执行：

```javascript
// Jenkins Execute NodeJS script 示例（NodeJS 18+）
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const DEPLOY_URL = process.env.DEPLOY_URL || 'http://localhost:8080/deploy';
const DEPLOY_SECRET = process.env.DEPLOY_SECRET || '';
const DEPLOY_CODE = process.env.DEPLOY_CODE || 'example';
const DEPLOY_FILE = process.env.DEPLOY_FILE || '';

(async () => {
  try {
    if (!DEPLOY_SECRET) throw new Error('缺少 DEPLOY_SECRET 环境变量');
    if (!/^[A-Za-z0-9_-]+$/.test(DEPLOY_CODE)) throw new Error('DEPLOY_CODE 非法，仅允许字母、数字、-、_');

    const timestamp = Date.now().toString();
    const sign = crypto.createHash('md5').update(DEPLOY_SECRET + DEPLOY_CODE + timestamp).digest('hex');

    const form = new FormData();
    form.append('code', DEPLOY_CODE);
    form.append('timestamp', timestamp);
    form.append('sign', sign);

    if (DEPLOY_FILE) {
      if (!fs.existsSync(DEPLOY_FILE)) throw new Error(`文件不存在: ${DEPLOY_FILE}`);
      const buf = fs.readFileSync(DEPLOY_FILE);
      const blob = new Blob([buf]);
      form.append('file', blob, path.basename(DEPLOY_FILE));
    }

    console.log(`POST ${DEPLOY_URL}`);
    console.log(`code=${DEPLOY_CODE}, timestamp=${timestamp}, sign=${sign}`);
    if (DEPLOY_FILE) console.log(`file=${DEPLOY_FILE}`);

    const res = await fetch(DEPLOY_URL, { method: 'POST', body: form });
    const text = await res.text();
    console.log(`status=${res.status}`);
    console.log('--- response ---');
    console.log(text);
    if (!res.ok) throw new Error(`接口返回非200：${res.status}`);
  } catch (err) {
    console.error('调用 /deploy 失败：', err.message || err);
    process.exit(1);
  }
})();
```

## 使用 Docker 打包运行
项目已提供 `Dockerfile`，可直接构建镜像：

```bash
cd deployagent
docker build -t yourname/deployagent:latest .
docker run -p 8080:8080 \
  -v $PWD/scripts:/app/scripts \
  -v $PWD/uploads:/app/uploads \
  -v $PWD/logs:/app/logs \
  yourname/deployagent:latest
```

注意：容器内使用 bash/sh 执行脚本。请确保你的脚本与运行环境兼容。

## GitHub Actions 自动构建并推送到 Docker Hub
已提供工作流文件，推送到 main 分支或打 tag（如 `v1.0.0`）将自动构建并推送镜像。请在 GitHub 仓库中设置 Secrets：
- `DOCKERHUB_USERNAME`
- `DOCKERHUB_TOKEN`（Docker Hub Access Token）
- 可选：`IMAGE_NAME`（默认 `deployagent`）

推送后镜像标签：
- 分支 push：`DOCKERHUB_USERNAME/IMAGE_NAME:latest` 与 `:commitSha`
- tag push：`DOCKERHUB_USERNAME/IMAGE_NAME:tag`
