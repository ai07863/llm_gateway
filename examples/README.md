# examples

最小接入示例。

- `python_openai_client.py`：用 OpenAI Python SDK 通过 llm-gateway 发起**非流式 + 流式**对话。

运行：

```bash
pip install openai
# PowerShell
$env:GATEWAY_TOKEN = "my-secret-123"
python examples/python_openai_client.py
```

默认连接 `http://localhost:8080/v1`，可用环境变量 `GATEWAY_BASE_URL` / `GATEWAY_TOKEN` / `GATEWAY_MODEL` 覆盖。