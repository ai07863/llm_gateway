"""最小示例：通过 llm-gateway 调用大模型（非流式 + 流式）。

用法：
    pip install openai

    # Windows PowerShell
    $env:GATEWAY_TOKEN = "my-secret-123"
    python examples/python_openai_client.py

    # macOS / Linux
    GATEWAY_TOKEN=my-secret-123 python examples/python_openai_client.py

前提：网关已在 http://localhost:8080 运行（见仓库根 README 的“快速开始”）。
可用环境变量覆盖默认值：GATEWAY_BASE_URL / GATEWAY_TOKEN / GATEWAY_MODEL。
"""

import os

from openai import OpenAI

BASE_URL = os.getenv("GATEWAY_BASE_URL", "http://localhost:8080/v1")
API_KEY = os.getenv("GATEWAY_TOKEN", "change-me")
MODEL = os.getenv("GATEWAY_MODEL", "qwen-plus")


def main() -> None:
    client = OpenAI(base_url=BASE_URL, api_key=API_KEY)

    print("== 非流式 ==")
    resp = client.chat.completions.create(
        model=MODEL,
        messages=[{"role": "user", "content": "用一句话解释什么是网关"}],
    )
    print(resp.choices[0].message.content)

    print("\n== 流式 ==")
    stream = client.chat.completions.create(
        model=MODEL,
        messages=[{"role": "user", "content": "写一个 Go 的 hello world"}],
        stream=True,
    )
    for chunk in stream:
        delta = chunk.choices[0].delta.content if chunk.choices else None
        if delta:
            print(delta, end="", flush=True)
    print()


if __name__ == "__main__":
    main()