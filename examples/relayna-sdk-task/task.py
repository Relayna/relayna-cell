#!/usr/local/bin/python3
"""Simple JSON handler for a fixed, offline SDK task image."""

import json
import sys


def main():
    payload = sys.stdin.buffer.read()
    if not isinstance(json.loads(payload, parse_float=str, parse_int=str), dict):
        raise ValueError("expected an object payload")
    sys.stdout.buffer.write(b'{"echo":' + payload + b'}\n')


if __name__ == "__main__":
    main()
