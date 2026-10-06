#!/usr/bin/env python3
"""
从 e2e 脚本的响应文件里按点号路径取值。

用法：jget.py data.token data.user.role

单独成文件而不是内联在 shell 里：
内联 python 配三引号在 shell 里转义极其脆弱，
嵌套引号就会语法错，而且排查起来看不出是脚本问题还是 JSON 问题。

读 /tmp/e2e_body.json —— 由 e2e.sh 的 expect 写入。
取不到时输出空串而不是报错：调用方只需判断空/非空。
"""
import json
import sys

SRC = "/tmp/e2e_body.json"


def main() -> int:
    if len(sys.argv) < 2:
        return 0
    path = sys.argv[1]
    try:
        with open(SRC, encoding="utf-8") as f:
            cur = json.load(f)
        for key in path.split("."):
            if key == "":
                continue
            cur = cur[key]
        # None 转空串：调用方统一用 -n / -z 判断
        print("" if cur is None else cur)
    except Exception:
        print("")
    return 0


if __name__ == "__main__":
    sys.exit(main())
