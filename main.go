package zlog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	caddy "github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/logging"
	"github.com/dustin/go-humanize"
)

const (
	// 512KB
	MaxBufferSize = 512 * 1024
	MaxTextSize   = 512
)

func init() {
	caddy.RegisterModule(&ZLog{})
	httpcaddyfile.RegisterHandlerDirective("zlog", parseCaddyfile)
}

// ZLog 插件打印请求到特定目录
// caddy 插件要求尽量大写
type ZLog struct {
	FileWriter logging.FileWriter
	LogFile    io.WriteCloser
	FileName   string
}

func (z *ZLog) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID: "http.handlers.zlog",
		New: func() caddy.Module {
			return &ZLog{}
		},
	}
}

func (z *ZLog) UnmarshalCaddyfile(d *caddyfile.Dispenser) (err error) {
	fw := &z.FileWriter
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			switch d.Val() {
			case "file_name":
				if !d.AllArgs(&fw.Filename) {
					return d.ArgErr()
				}
				z.FileName = fw.Filename
			case "roll_disabled":
				var f bool
				fw.Roll = &f
				if d.NextArg() {
					return d.ArgErr()
				}

			case "roll_size":
				var sizeStr string
				if !d.AllArgs(&sizeStr) {
					return d.ArgErr()
				}
				size, err := humanize.ParseBytes(sizeStr)
				if err != nil {
					return d.Errf("parsing size: %v", err)
				}
				fw.RollSizeMB = int(math.Ceil(float64(size) / humanize.MiByte))

			case "roll_uncompressed":
				var f bool
				fw.RollCompress = &f
				if d.NextArg() {
					return d.ArgErr()
				}

			case "roll_local_time":
				fw.RollLocalTime = true
				if d.NextArg() {
					return d.ArgErr()
				}

			case "roll_keep":
				var keepStr string
				if !d.AllArgs(&keepStr) {
					return d.ArgErr()
				}
				keep, err := strconv.Atoi(keepStr)
				if err != nil {
					return d.Errf("parsing roll_keep number: %v", err)
				}
				fw.RollKeep = keep

			case "roll_keep_for":
				var keepForStr string
				if !d.AllArgs(&keepForStr) {
					return d.ArgErr()
				}
				keepFor, err := caddy.ParseDuration(keepForStr)
				if err != nil {
					return d.Errf("parsing roll_keep_for duration: %v", err)
				}
				if keepFor < 0 {
					return d.Errf("negative roll_keep_for duration: %v", keepFor)
				}
				fw.RollKeepDays = int(math.Ceil(keepFor.Hours() / 24))
			}
		}
	}
	z.printCfg()
	return nil
}

type proxyWriter struct {
	http.ResponseWriter
	buf     bytes.Buffer
	code    int
	req     *http.Request
	body    io.ReadCloser
	bodyBuf bytes.Buffer
	respLen int
}

func (pw *proxyWriter) Read(p []byte) (n int, err error) {
	n, err = pw.body.Read(p)
	if pw.req.ContentLength > MaxBufferSize {
		return
	}
	// 不记录非 ascii 字符
	for i := 0; i < n; i++ {
		if p[i] > 127 {
			return
		}
	}

	pw.bodyBuf.Write(p[:n])
	return
}

func (z *ZLog) printCfg() {
	data, _ := json.Marshal(z.FileWriter)
	fmt.Printf("zlog cfg = %s\n", data)
}

func (pw *proxyWriter) Close() error {
	return pw.body.Close()
}

func (p *proxyWriter) WriteHeader(statusCode int) {
	p.ResponseWriter.WriteHeader(statusCode)
	p.code = statusCode
}
func (p *proxyWriter) Write(data []byte) (n int, err error) {
	n, err = p.ResponseWriter.Write(data)
	p.respLen += n
	if p.buf.Len() >= MaxBufferSize {
		return
	}
	p.buf.Write(data)
	return
}

func (p *proxyWriter) tryToJson(buf bytes.Buffer) (out string) {
	out = buf.String()
	defer func() {
		if len(out) > MaxTextSize {
			out = out[:MaxTextSize]
		}
	}()
	var (
		jsonObj interface{}
		err     error
	)

	if err = json.Unmarshal([]byte(out), &jsonObj); err != nil {
		return strings.ReplaceAll(out, "\n", "\\n")
	}
	data, _ := json.Marshal(jsonObj)
	return string(data)
}

func (p *proxyWriter) writeLog(d time.Duration, w io.Writer) {
	now := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(w, "%s %s %d %s %s %s", now, d.String(), p.code, p.req.Method, p.req.URL.Path, p.req.Header.Get("Content-Type"))

	if p.req.ContentLength > MaxBufferSize {
		w.Write([]byte(" [Large Request] "))
	} else {
		fmt.Fprintf(w, " %s", p.tryToJson(p.bodyBuf))
	}

	if p.respLen > MaxBufferSize {
		w.Write([]byte(" [Large Response] "))
	} else {
		fmt.Fprintf(w, " %s", p.tryToJson(p.buf))
	}

	w.Write([]byte(" \n"))
}

// ServeHTTP 打印日志
// 格式 = 时间 + Code + 请求方法 + PATH + HOSTNAME + 路径 + 请求体 + 响应体
func (z *ZLog) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) (err error) {
	start := time.Now()
	writer := proxyWriter{
		ResponseWriter: w,
		req:            r,
		body:           r.Body,
	}
	r.Body = &writer

	err = next.ServeHTTP(&writer, r)
	end := time.Now()
	if z.LogFile != nil {
		var buf bytes.Buffer
		writer.writeLog(end.Sub(start), &buf)
		s := buf.String()
		z.LogFile.Write([]byte(s))
		os.Stdout.Write([]byte(s))
	}
	return
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var zlog ZLog
	err := zlog.UnmarshalCaddyfile(h.Dispenser)
	return &zlog, err
}

// Provision implements caddy.Provisioner.
func (z *ZLog) Provision(ctx caddy.Context) error {
	z.LogFile, _ = z.FileWriter.OpenWriter()
	return nil
}

// Validate implements caddy.Validator.
func (z *ZLog) Validate() error {
	return nil
}

func (z *ZLog) Cleanup() error {
	if z.LogFile != nil {
		z.LogFile.Close()
	}
	return nil
}

var (
	_ caddy.Provisioner           = (*ZLog)(nil)
	_ caddy.Validator             = (*ZLog)(nil)
	_ caddyhttp.MiddlewareHandler = (*ZLog)(nil)
	_ caddyfile.Unmarshaler       = (*ZLog)(nil)
)
