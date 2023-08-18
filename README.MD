# 使用方法

```Caddyfile
http://localhost:80 {
    zlog {
		file_name /var/log/szdaji/access.log # 日志名称前缀
		roll_size 32Mib # 滚动日志
		roll_uncompressed # 不要压缩日志
		roll_local_time  # 日志文件时间用本地时区
		truncate 128B # 对大的请求/响应body截断
	} 
	reverse_proxy http://127.0.0.1:8080
}
```
xcaddy build --with github.com/Salpadding/zlog
