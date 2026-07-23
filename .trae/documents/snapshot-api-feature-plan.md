# 视频抓拍 API 功能实现计划（CGO + ffmpeg 方案）

## 摘要

为 MediaMTX 新增通过 API 接口触发视频抓拍的功能。利用项目已有的 CGO + ffmpeg 基础设施（`internal/codec/`），直接调用 ffmpeg 动态库进行 H.264/H.265 → JPEG 转码，抓拍成功后返回图片路径。

---

## 当前状态分析

### 关键基础设施
- **CGO + ffmpeg**：项目已有完整的 CGO 模式，见 [`internal/codec/transcoder-windows-amd64.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/codec/transcoder-windows-amd64.go)、[`transcoder-linux-amd64.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/codec/transcoder-linux-amd64.go)、[`transcoder-linux-arm64.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/codec/transcoder-linux-arm64.go)
  - 已链接 `avcodec`、`avutil`、`swscale`、`swresample`
  - 已有 `AudioTranscoder` 实现音频解码 + 编码 + 重采样的完整流程
  - ffmpeg 动态库（.dll/.so）在 `thirdparty/ffmpeg-n6.1.2-*/lib/` 下已分发
- **API 服务器**：[`internal/api/api.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/api/api.go) 使用 Gin，路由挂载在 `/v3`
- **Path 管理**：[`internal/core/path_manager.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/core/path_manager.go) 通过 channel 模式管理 path，已有 `APIPathStartRecording` / `APIPathStopRecording` 等 API 方法
- **Path 事件循环**：[`internal/core/path.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/core/path.go) 持有 `pa.stream *stream.Stream`
- **Stream 读取**：[`internal/stream/stream.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/stream/stream.go) 提供 `AddReader` / `StartReader` / `RemoveReader`
- **H.264/H.265 单元**：通过 `unit` 包定义，包含 NAL 数据
- **配置系统**：[`internal/conf/conf.go`](file:///f:/09.SourceCode/07.VideoFusion/mediamtx/internal/conf/conf.go)

---

## 设计方案

### API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/v3/snapshots/capture/*name` | 触发抓拍，调用 CGO ffmpeg 解码+编码 |
| `GET` | `/v3/snapshots/file/*filepath` | 获取已保存的 JPEG 图片 |

**请求：**
```
POST /v3/snapshots/capture/cam1
```

**成功响应（200）：**
```json
{
  "imagePath": "snapshots/cam1/2026-07-22/15-30-45.jpg"
}
```

### 存储目录

```
./snapshots/
└── <pathName>/
    └── <YYYY-MM-DD>/
        └── <HH-MM-SS>.jpg
```

### 架构流程

```
POST /v3/snapshots/capture/cam1
  → api.onSnapshotsCapture()
    → pathManager.APIPathSnapshot("cam1")
      → path goroutine (事件循环)
        → 订阅 stream reader (H.264/H.265 track)
        → 将 NAL units 喂给 codec.VideoTranscoder
        → VideoTranscoder 解码 → 转 RGB/YUV → 编码 JPEG
        → 写入磁盘 → 返回文件路径
```

---

## 实现步骤

### Step 1: 添加全局配置项 `snapshotDir`

**文件：** `internal/conf/conf.go`

在 `Conf` 结构体添加：
```go
SnapshotDir string `json:"snapshotDir"`
```

默认值（`setDefaults` 中）：
```go
conf.SnapshotDir = "./snapshots"
```

### Step 2: 扩展 `internal/codec/` 添加 `VideoTranscoder`

**文件：** `internal/codec/transcoder-windows-amd64.go`、`transcoder-linux-amd64.go`、`transcoder-linux-arm64.go`

三个平台文件中添加相同的 Go 代码（因 CGO 指令不同需分别添加）：

#### 新增结构体 `VideoTranscoder`

```go
type VideoTranscoder struct {
    decCtx   *C.AVCodecContext
    decFrame *C.AVFrame
    decPkt   *C.AVPacket

    encCtx   *C.AVCodecContext
    encPkt   *C.AVPacket

    swsCtx    *C.struct_SwsContext
    swsFrame  *C.AVFrame
    swsBuf    []byte
}
```

#### 新增方法

- `Initialize(width, height int, inputCodec C.enum_AVCodecID)` — 初始化解码器（H.264/H.265）和 MJPEG 编码器
- `DecodeAndEncode(nalData []byte) ([]byte, error)` — 输入 NAL 数据，输出 JPEG 字节
- `Close()` — 释放资源

核心流程：
1. **解码**：`avcodec_send_packet` → `avcodec_receive_frame` 得到 `AVFrame`（YUV420P）
2. **格式转换**：`sws_scale` YUV420P → RGB24
3. **编码 JPEG**：`avcodec_send_frame` → `avcodec_receive_packet` 得到 JPEG `AVPacket`
4. 返回 `packet.data` 的 Go 字节切片

关键编码参数：
```c
encCtx->pix_fmt = AV_PIX_FMT_YUVJ420P;  // MJPEG encoder input
encCtx->time_base = (AVRational){1, 1};
encCtx->width = width;
encCtx->height = height;
```

### Step 3: 扩展 `internal/defs/api.go` 接口

**文件：** `internal/defs/api.go`

在 `APIPathManager` 接口添加：
```go
type APIPathManager interface {
    // ... 已有方法 ...
    APIPathSnapshot(string) (*APIPathSnapshot, error)
}
```

新增响应类型：
```go
type APIPathSnapshot struct {
    ImagePath string `json:"imagePath"`
}
```

### Step 4: 在 `path.go` 中添加抓拍处理

**文件：** `internal/core/path.go`

#### 4.1 新增请求/响应类型
```go
type pathAPISnapshotRes struct {
    imagePath string
    err       error
}

type pathAPISnapshotReq struct {
    name        string
    snapshotDir string
    res         chan pathAPISnapshotRes
}
```

#### 4.2 在 path 结构体中添加 channel
```go
chAPISnapshot chan pathAPISnapshotReq
```

#### 4.3 `initialize()` 中初始化
```go
pa.chAPISnapshot = make(chan pathAPISnapshotReq)
```

#### 4.4 `runInner()` 中添加 case
```go
case req := <-pa.chAPISnapshot:
    pa.doAPISnapshot(req)
```

#### 4.5 实现 `doAPISnapshot`

核心逻辑：
1. 检查 `pa.stream != nil`
2. 遍历 `pa.stream.Desc.Medias` 找到第一个视频 track（H.264 或 H.265）
3. 创建 `snapshotReader`（实现 `stream.Reader` / `logger.Writer`）
4. 初始化 `codec.VideoTranscoder`
5. 调用 `pa.stream.AddReader` + `StartReader` 订阅视频流
6. 在 ReadFunc 回调中：
   - 将 `unit.H264.AU` 或 `unit.H265.AU` 数据喂给 `VideoTranscoder.DecodeAndEncode()`
   - 一旦获得 JPEG 字节，通过 channel 通知完成
   - **仅做数据拷贝和信号通知，不在此回调中做文件 I/O**
7. 收到 JPEG 后：`RemoveReader` → 创建目录 → 写入文件 → 返回路径
8. 生成文件路径：`<snapshotDir>/<pathName>/<YYYY-MM-DD>/<HH-MM-SS>.jpg`

### Step 5: 在 `path_manager.go` 添加转发

**文件：** `internal/core/path_manager.go`

#### 5.1 添加 channel + 字段
```go
chAPISnapshot chan pathAPISnapshotReq
snapshotDir   string
```

#### 5.2 `initialize()` 初始化
```go
pm.chAPISnapshot = make(chan pathAPISnapshotReq)
```

#### 5.3 `run()` 添加 case
```go
case req := <-pm.chAPISnapshot:
    pm.doAPIPathSnapshot(req)
```

#### 5.4 实现方法
```go
func (pm *pathManager) APIPathSnapshot(name string) (*defs.APIPathSnapshot, error) {
    req := pathAPISnapshotReq{
        name:        name,
        snapshotDir: pm.snapshotDir,
        res:         make(chan pathAPISnapshotRes),
    }
    select {
    case pm.chAPISnapshot <- req:
        res := <-req.res
        if res.err != nil {
            return nil, res.err
        }
        return &defs.APIPathSnapshot{ImagePath: res.imagePath}, nil
    case <-pm.ctx.Done():
        return nil, fmt.Errorf("terminated")
    }
}
```

### Step 6: 在 `api.go` 添加 API 端点

**文件：** `internal/api/api.go`

#### 6.1 注册路由
```go
group.POST("/snapshots/capture/*name", a.onSnapshotsCapture)
group.GET("/snapshots/file/*filepath", a.onSnapshotsFile)
```

#### 6.2 实现 `onSnapshotsCapture`

```go
func (a *API) onSnapshotsCapture(ctx *gin.Context) {
    pathName, ok := paramName(ctx)
    if !ok {
        a.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid name"))
        return
    }
    data, err := a.PathManager.APIPathSnapshot(pathName)
    if err != nil {
        if errors.Is(err, conf.ErrPathNotFound) {
            a.writeError(ctx, http.StatusNotFound, err)
        } else {
            a.writeError(ctx, http.StatusBadRequest, err)
        }
        return
    }
    ctx.JSON(http.StatusOK, data)
}
```

#### 6.3 实现 `onSnapshotsFile`

```go
func (a *API) onSnapshotsFile(ctx *gin.Context) {
    filePath := ctx.Param("filepath")
    if len(filePath) < 2 || filePath[0] != '/' {
        a.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid file path"))
        return
    }
    fullPath := filepath.Join(a.Conf.SnapshotDir, filePath[1:])
    // 路径安全检查
    absSnapshotDir, _ := filepath.Abs(a.Conf.SnapshotDir)
    absFullPath, _ := filepath.Abs(fullPath)
    if !strings.HasPrefix(absFullPath, absSnapshotDir) {
        a.writeError(ctx, http.StatusBadRequest, fmt.Errorf("invalid file path"))
        return
    }
    ctx.File(fullPath)
}
```

### Step 7: 在 `core.go` 传入配置

**文件：** `internal/core/core.go`

创建 pathManager 时传入：
```go
p.pathManager = &pathManager{
    // ... 已有字段 ...
    snapshotDir: p.conf.SnapshotDir,
}
```

### Step 8: 更新 YAML 配置

**文件：** `mediamtx.yml`

```yaml
# 抓拍图片存储目录。
snapshotDir: ./snapshots
```

---

## 关键设计决策

1. **复用 CGO 模式**：遵循项目已有的 `AudioTranscoder` 架构，在 `internal/codec/` 中新增 `VideoTranscoder`，使用相同的 CGO 链接方式
2. **Stream reader 集成**：通过 path goroutine 订阅自己的 stream，直接获取 H.264/H.265 NAL 数据，避免 RTSP 回环
3. **回调轻量化**：ReadFunc 回调中仅做数据拷贝和信号通知，文件 I/O 在 path goroutine 中异步完成，避免持有 stream 锁时执行磁盘写入
4. **Keyframe 处理**：H.264/H.265 解码需要从 IDR 帧开始，VideoTranscoder 会跳过非关键帧直到收到第一个 IDR
5. **超时保护**：path goroutine 设置超时（如 10 秒），超时后移除 reader 并返回错误

## 假设与风险

- 流中存在视频 track（H.264 或 H.265 格式）
- 首个 IDR 关键帧能在超时时间内到达（通常 2-5 秒内）
- CGO 编译环境就绪（项目已满足，Windows/Linux 均已有 CGO 配置）
- 如果不支持视频编码格式，返回明确错误信息

## 影响范围

| 文件 | 改动类型 |
|------|---------|
| `internal/codec/transcoder-windows-amd64.go` | 新增 VideoTranscoder |
| `internal/codec/transcoder-linux-amd64.go` | 新增 VideoTranscoder |
| `internal/codec/transcoder-linux-arm64.go` | 新增 VideoTranscoder |
| `internal/conf/conf.go` | 新增 SnapshotDir 字段 |
| `internal/defs/api.go` | 新增接口 + 响应类型 |
| `internal/core/path.go` | 新增 snapshot 处理逻辑 |
| `internal/core/path_manager.go` | 新增 snapshot 转发 |
| `internal/api/api.go` | 新增 API 端点 |
| `internal/core/core.go` | 传入 snapshotDir 配置 |
| `mediamtx.yml` | 新增配置项 |

## 验证步骤

1. 启动 MediaMTX，确认有 H.264/H.265 视频流推流
2. 调用 `POST /v3/snapshots/capture/<pathName>` 触发抓拍
3. 验证返回 200 和正确的 `imagePath`
4. 调用 `GET /v3/snapshots/file/<imagePath>` 获取 JPEG，确认是有效图片
5. 检查 `./snapshots/<pathName>/<YYYY-MM-DD>/` 下有对应文件
6. 测试异常：流不存在(404)、无视频track(400)、超时(500)
