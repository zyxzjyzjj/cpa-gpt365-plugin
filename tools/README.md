# 辅助工具

这两个工具与插件解耦，可独立构建运行，用于在没有 CPA 的情况下验证关键前提。

## chainprobe —— 两级代理链验证

验证「本机 -> 本地代理 -> 远程轮换代理 -> 目标」是否真的成立。

```bash
cd tools/chainprobe
go run .
```

输出包含四组结论：

1. 仅经本地代理访问目标（对照组）
2. 经本地代理嵌套 CONNECT 访问远程轮换代理（实验组）
3. 远程代理口令故意写错（**负向对照，必须失败**）
4. 连续多次请求的出口 IP（确认轮换生效）

第 3 项是关键：如果错误口令也能成功，说明第二跳根本没执行，
所谓「两级链路」是假的。`curl` 的 `--preproxy` + `--proxy` 组合在部分
平台上并不会真正嵌套，因此必须用这个工具确认。

## abicheck —— 插件 ABI 加载验证

模拟 CPA 宿主 `dlopen` 插件并调用 ABI，验证导出符号与 JSON 信封契约。

```bash
cd tools/abicheck
go build -o abicheck.exe .
./abicheck.exe ../../build/windows/amd64/openai-basispoints.dll
```

校验项：

- 四个导出符号存在（`cliproxy_plugin_init`、`cliproxyPluginCall`、
  `cliproxyPluginFree`、`cliproxyPluginShutdown`）
- 宿主回调缺失时 `init` 拒绝初始化（返回非 0）
- 宿主回调就绪时 `init` 成功并声明 ABI 版本
- `auth.identifier` / `executor.identifier` / `model.register` / `plugin.register`
  返回合法信封
- 未知方法返回明确的错误信封而非静默成功

仅支持 Windows；其他平台可用 `dlopen` 改写。
