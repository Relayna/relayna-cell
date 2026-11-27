#!/usr/local/bin/python3
"""Call the host-mediated endpoint from a task without credentials."""

import json
import sys
import urllib.request


def main():
    payload = json.load(sys.stdin)
    request = urllib.request.Request(
        "http://task-inference.cell/v1/chat/completions",
        data=json.dumps({
            "model": payload["model"],
            "messages": [{"role": "user", "content": payload["prompt"]}],
            "max_tokens": 64,
            "stream": False,
        }).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=45) as response:
        completion = json.load(response)
    json.dump({"text": completion["choices"][0]["message"]["content"]}, sys.stdout)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
