#!/usr/bin/env python3
"""
SSH/SFTP 工具（部署一次性用）。

为什么用 paramiko 而不是 expect 或 sshpass：
  - expect 在本执行环境里被沙箱限制，跑不通
  - sshpass 需要安装，macOS 不自带
  - paramiko 是纯 Python 库，可控且能拿到退出码

密码从环境变量读，不进命令行（会出现在 ps 输出里）。

子命令：
  run    <host> <cmd>            远程执行
  put    <host> <本地路径> <远端路径>   上传
  get    <host> <远端路径> <本地路径>   下载
"""
import os
import sys

import paramiko


def connect(host, timeout=20):
    password = os.environ.get("SSHX_PASSWORD", "")
    if not password:
        sys.exit("需设置 SSHX_PASSWORD")
    c = paramiko.SSHClient()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    c.connect(
        host,
        username=os.environ.get("SSHX_USER", "root"),
        password=password,
        timeout=timeout,
        banner_timeout=timeout,
        auth_timeout=timeout,
    )
    return c


def cmd_run(host, command, timeout=300):
    c = connect(host)
    try:
        stdin, stdout, stderr = c.exec_command(command, timeout=timeout)
        out = stdout.read().decode("utf-8", "replace")
        err = stderr.read().decode("utf-8", "replace")
        code = stdout.channel.recv_exit_status()
        if out:
            sys.stdout.write(out)
        if err.strip():
            sys.stderr.write(err)
        return code
    finally:
        c.close()


def cmd_put(host, local, remote):
    c = connect(host)
    try:
        sftp = c.open_sftp()
        sftp.put(local, remote)
        sftp.close()
        return 0
    finally:
        c.close()


def cmd_get(host, remote, local):
    c = connect(host)
    try:
        sftp = c.open_sftp()
        sftp.get(remote, local)
        sftp.close()
        return 0
    finally:
        c.close()


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    action, host = sys.argv[1], sys.argv[2]
    if action == "run":
        return cmd_run(host, sys.argv[3])
    if action == "put":
        return cmd_put(host, sys.argv[3], sys.argv[4])
    if action == "get":
        return cmd_get(host, sys.argv[3], sys.argv[4])
    print(f"未知子命令: {action}")
    return 2


if __name__ == "__main__":
    sys.exit(main())
