package main

import (
    "bufio"
    "crypto/md5"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strconv"
    "strings"
    "sync"
    "time"

    "gopkg.in/yaml.v3"
)

type Config struct {
    Server struct {
        Addr           string `yaml:"addr"`
        Secret         string `yaml:"secret"`
        UploadDir      string `yaml:"upload_dir"`
        ScriptDir      string `yaml:"script_dir"`
        LogsDir        string `yaml:"logs_dir"`
        MaxUploadBytes int64  `yaml:"max_upload_bytes"`
    } `yaml:"server"`
    Auth struct {
        Users []struct {
            Username string `yaml:"username"`
            Password string `yaml:"password"`
        } `yaml:"users"`
    } `yaml:"auth"`
}

var (
    cfg             Config
    once            sync.Once
    validCodeRegexp = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func loadConfig() {
    once.Do(func() {
        f, err := os.Open("config.yaml")
        if err != nil {
            log.Fatalf("无法读取配置文件: %v", err)
        }
        defer f.Close()
        dec := yaml.NewDecoder(f)
        if err := dec.Decode(&cfg); err != nil {
            log.Fatalf("配置解析失败: %v", err)
        }
        mustMkdir(cfg.Server.UploadDir)
        mustMkdir(cfg.Server.ScriptDir)
        mustMkdir(cfg.Server.LogsDir)
    })
}

func mustMkdir(dir string) {
    if dir == "" {
        return
    }
    if err := os.MkdirAll(dir, 0o755); err != nil {
        log.Fatalf("创建目录失败 %s: %v", dir, err)
    }
}

func calcMD5Hex(s string) string {
    sum := md5.Sum([]byte(s))
    return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(v)
}

func checkLogin(r *http.Request) bool {
    // 简单 cookie 会话：login=1 表示已登录
    c, err := r.Cookie("login")
    return err == nil && c.Value == "1"
}

func requireLogin(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if strings.HasPrefix(r.URL.Path, "/web/") || r.URL.Path == "/" || r.URL.Path == "/api/login" {
            next(w, r)
            return
        }
        if !checkLogin(r) {
            http.Redirect(w, r, "/web/index.html", http.StatusFound)
            return
        }
        next(w, r)
    }
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }
    var body struct {
        Username string `json:"username"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        w.WriteHeader(http.StatusBadRequest)
        return
    }
    ok := false
    for _, u := range cfg.Auth.Users {
        if u.Username == body.Username && u.Password == body.Password {
            ok = true
            break
        }
    }
    if !ok {
        w.WriteHeader(http.StatusUnauthorized)
        return
    }
    http.SetCookie(w, &http.Cookie{Name: "login", Value: "1", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
    w.WriteHeader(http.StatusOK)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
    http.SetCookie(w, &http.Cookie{Name: "login", Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1})
    w.WriteHeader(http.StatusOK)
}

func handleDeploy(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }

    // 限制请求体大小
    r.Body = http.MaxBytesReader(w, r.Body, cfg.Server.MaxUploadBytes)
    if err := r.ParseMultipartForm(cfg.Server.MaxUploadBytes); err != nil {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("上传体解析失败"))
        return
    }

    code := r.FormValue("code")
    ts := r.FormValue("timestamp")
    sign := r.FormValue("sign")
    if !validCodeRegexp.MatchString(code) {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("非法code"))
        return
    }

    // 签名校验 md5(secret+code+timestamp)
    expect := calcMD5Hex(cfg.Server.Secret + code + ts)
    if !strings.EqualFold(expect, sign) {
        w.WriteHeader(http.StatusUnauthorized)
        _, _ = w.Write([]byte("签名无效"))
        return
    }
    // 时间戳窗口校验（5分钟）
    if ms, err := strconv.ParseInt(ts, 10, 64); err == nil {
        if delta := time.Since(time.UnixMilli(ms)); delta < -5*time.Minute || delta > 5*time.Minute {
            w.WriteHeader(http.StatusUnauthorized)
            _, _ = w.Write([]byte("时间戳过期"))
            return
        }
    }

    var uploadedPath string
    file, header, err := r.FormFile("file")
    if err == nil && file != nil {
        defer file.Close()
        safeName := filepath.Base(header.Filename)
        dst := filepath.Join(cfg.Server.UploadDir, fmt.Sprintf("%d_%s", time.Now().UnixNano(), safeName))
        out, err := os.Create(dst)
        if err != nil {
            w.WriteHeader(http.StatusInternalServerError)
            _, _ = w.Write([]byte("保存文件失败"))
            return
        }
        defer out.Close()
        if _, err := io.Copy(out, file); err != nil {
            w.WriteHeader(http.StatusInternalServerError)
            _, _ = w.Write([]byte("写入失败"))
            return
        }
        uploadedPath = dst
    }

    // 执行脚本 {code}.sh 并传递文件路径
    script := filepath.Join(cfg.Server.ScriptDir, code+".sh")
    if _, err := os.Stat(script); err != nil {
        w.WriteHeader(http.StatusBadRequest)
        _, _ = w.Write([]byte("脚本不存在"))
        return
    }

    // 选择可用的 shell
    shell := ""
    if p, err := exec.LookPath("bash"); err == nil {
        shell = p
    } else if p, err := exec.LookPath("sh"); err == nil {
        shell = p
    } else {
        http.Error(w, "未找到 bash/sh，请安装 Git Bash 或提供可用的 shell", http.StatusInternalServerError)
        return
    }

    cmd := exec.Command(shell, script, uploadedPath)
    cmd.Env = append(os.Environ(), "UPLOAD_FILE="+uploadedPath, "CODE="+code)
    stdout, err := cmd.StdoutPipe()
    if err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }
    stderr, err := cmd.StderrPipe()
    if err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }
    if err := cmd.Start(); err != nil { http.Error(w, err.Error(), http.StatusInternalServerError); return }

    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    flusher, _ := w.(http.Flusher)
    stream := func(prefix string, r io.Reader) {
        scanner := bufio.NewScanner(r)
        for scanner.Scan() {
            fmt.Fprintf(w, "%s%s\n", prefix, scanner.Text())
            if flusher != nil { flusher.Flush() }
        }
    }
    go stream("", stdout)
    stream("[ERR] ", stderr)
    _ = cmd.Wait()
}

func handleSign(w http.ResponseWriter, r *http.Request) {
    if !checkLogin(r) { w.WriteHeader(http.StatusUnauthorized); return }
    code := r.URL.Query().Get("code")
    if !validCodeRegexp.MatchString(code) {
        http.Error(w, "非法code", http.StatusBadRequest)
        return
    }
    ts := fmt.Sprintf("%d", time.Now().UnixMilli())
    sign := calcMD5Hex(cfg.Server.Secret + code + ts)
    writeJSON(w, 200, map[string]string{"timestamp": ts, "sign": sign})
}

func listLogFiles() ([]string, error) {
    entries, err := os.ReadDir(cfg.Server.LogsDir)
    if err != nil { return nil, err }
    var files []string
    for _, e := range entries {
        if e.IsDir() { continue }
        name := e.Name()
        if strings.HasPrefix(name, ".") { continue }
        files = append(files, name)
    }
    return files, nil
}

func handleLogsList(w http.ResponseWriter, r *http.Request) {
    if !checkLogin(r) { w.WriteHeader(http.StatusUnauthorized); return }
    files, err := listLogFiles()
    if err != nil { writeJSON(w, 500, map[string]any{"error": err.Error()}); return }
    writeJSON(w, 200, map[string]any{"files": files})
}

func safeLogPath(name string) (string, error) {
    if name == "" { return "", errors.New("空文件名") }
    if strings.Contains(name, "..") { return "", errors.New("非法文件名") }
    p := filepath.Join(cfg.Server.LogsDir, filepath.Base(name))
    return p, nil
}

func handleLogsView(w http.ResponseWriter, r *http.Request) {
    if !checkLogin(r) { w.WriteHeader(http.StatusUnauthorized); return }
    name := r.URL.Query().Get("file")
    start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
    n, _ := strconv.ParseInt(r.URL.Query().Get("n"), 10, 64)
    if n <= 0 { n = 100 }
    p, err := safeLogPath(name)
    if err != nil { http.Error(w, err.Error(), 400); return }
    f, err := os.Open(p)
    if err != nil { http.Error(w, "打开失败", 404); return }
    defer f.Close()

    // 简单逐行读取，跳过 start 行，输出 n 行
    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    scanner := bufio.NewScanner(f)
    var idx int64
    for scanner.Scan() {
        if idx >= start && idx < start+n {
            fmt.Fprintln(w, scanner.Text())
        }
        idx++
        if idx >= start+n { break }
    }
}

func handleLogsTail(w http.ResponseWriter, r *http.Request) {
    if !checkLogin(r) { w.WriteHeader(http.StatusUnauthorized); return }
    name := r.URL.Query().Get("file")
    p, err := safeLogPath(name)
    if err != nil { http.Error(w, err.Error(), 400); return }

    // SSE 实时输出
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    flusher, ok := w.(http.Flusher)
    if !ok { http.Error(w, "不支持SSE", 500); return }

    f, err := os.Open(p)
    if err != nil { http.Error(w, "打开失败", 404); return }
    defer f.Close()

    // 从末尾开始追踪
    offset, _ := f.Seek(0, io.SeekEnd)
    _ = offset

    reader := bufio.NewReader(f)
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()
    for {
        line, err := reader.ReadString('\n')
        if err == io.EOF {
            <-ticker.C
            continue
        }
        if err != nil {
            return
        }
        fmt.Fprintf(w, "data: %s\n\n", strings.TrimRight(line, "\n"))
        flusher.Flush()
    }
}

func handleDockerPs(w http.ResponseWriter, r *http.Request) {
    if !checkLogin(r) { w.WriteHeader(http.StatusUnauthorized); return }
    out, err := exec.Command("docker", "ps", "-a").CombinedOutput()
    if err != nil {
        http.Error(w, string(out), 500)
        return
    }
    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    _, _ = w.Write(out)
}

func handleDockerLogs(w http.ResponseWriter, r *http.Request) {
    if !checkLogin(r) { w.WriteHeader(http.StatusUnauthorized); return }
    id := r.URL.Query().Get("id")
    if id == "" { http.Error(w, "缺少id", 400); return }
    out, err := exec.Command("docker", "logs", "--tail", "200", id).CombinedOutput()
    if err != nil {
        http.Error(w, string(out), 500)
        return
    }
    w.Header().Set("Content-Type", "text/plain; charset=utf-8")
    _, _ = w.Write(out)
}

func main() {
    loadConfig()

    fs := http.FileServer(http.Dir("web"))
    http.Handle("/web/", http.StripPrefix("/web/", fs))
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        http.Redirect(w, r, "/web/index.html", http.StatusFound)
    })

    http.HandleFunc("/api/login", requireLogin(handleLogin))
    http.HandleFunc("/api/logout", requireLogin(handleLogout))
    // /deploy 开放，仅签名认证
    http.HandleFunc("/deploy", handleDeploy)

    http.HandleFunc("/api/logs", requireLogin(handleLogsList))
    http.HandleFunc("/api/logs/view", requireLogin(handleLogsView))
    http.HandleFunc("/api/logs/tail", requireLogin(handleLogsTail))

    http.HandleFunc("/api/docker/ps", requireLogin(handleDockerPs))
    http.HandleFunc("/api/docker/logs", requireLogin(handleDockerLogs))

    http.HandleFunc("/api/sign", requireLogin(handleSign))

    log.Printf("服务启动于 %s", cfg.Server.Addr)
    log.Fatal(http.ListenAndServe(cfg.Server.Addr, nil))
}


