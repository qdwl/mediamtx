# WS-FLV（H.265）播放绿屏 / 无法播放 问题分析与修复

| 项目 | 内容 |
|---|---|
| 现象 | `ws://<host>:59008/641438102182738059.flv?id=...` 有数据持续下发，但播放器不出画面 / 播放一段后绿屏 |
| 播放器 | `mpegts.js`（xqq/mpegts.js），HEVC 通常可正常播放 |
| 服务端 | 本项目（基于 MediaMTX 二次开发）`internal/servers/flv` + `internal/protocols/flv` |
| 码流来源 | GB28181（PS 流），H.265 Main，2688×1520 @25fps，AAC-LC 8000Hz 双声道 |
| 结论 | 一个图像被拆成多个 FLV tag（每个 tag 只有一个 slice），播放器按 `1 tag = 1 帧` 解码，只能解出第一个 slice，其余区域为绿；叠加同一 AU 内重复下发 VPS/SPS/PPS，触发 `PPS changed between slices` |
| 修复提交 | `pro`: `808e2612`，`dev`: `129a7185`（cherry-pick） |

---

## 一、问题现象

1. WS 连接可以建立，数据一直在下发（约 2.8 Mbps），FLV 头、`PreviousTagSize` 等容器字段都正确，**不是"没流"**。
2. 其它 H.265 摄像机的 WS-FLV 在同一播放器上可以正常播放，只有这一路不行。
3. 该路表现为：不显示画面，或播放很短时间后画面变绿/卡住。

---

## 二、判断分析过程

### 2.1 先证伪"传输层/容器层有问题"

用 Node 手写 WebSocket 客户端（`Sec-WebSocket-Key` 握手 + 解析服务端非掩码帧）连接该地址，抓 6 秒并解析 FLV：

```
--- handshake ---
HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
--- result ---
frames 610  bytes 2795797
sig FLV flags 101 dataOffset 9 prevTagSize0 0
```

- 握手正常；6 秒收 610 个二进制帧 / 2.79 MB；
- 逐 tag 校验 `PreviousTagSize == 11 + dataSize`：**0 个错误**。

结论：传输层、FLV 基本框架、时间戳字段的"格式"都没问题，问题在**流内容的结构**。

### 2.2 确认编码格式

```
ffprobe -v error -show_streams "http://<host>:59008/641438102182738059.flv?id=..."
```

```
Stream #0:0: Video: hevc (Main), 2688x1520, 25 fps
Stream #0:1: Audio: aac (LC), 8000 Hz, stereo
```

FLV 视频 tag 首字节是 `0x80`（序列头）/`0x83`（码流），即 **Enhanced RTMP / Enhanced FLV**，
FourCC 为 `hvc1`，序列头里是 `HEVCDecoderConfigurationRecord`（hvcC）。

> 说明：Enhanced-FLV 的 H.265 只有 ffmpeg ≥ 6.0、mpegts.js 新版本等少数实现支持；
> flv.js（2018 年后停止维护）、VLC 3.x 等只认 `codecId = 7`(AVC)，遇到 `0x8x` 会直接丢弃视频。
> 这是"为什么有些播放器完全没画面"的第一层原因，但**不是本次问题的根因**（该用户用的是 mpegts.js）。

### 2.3 解剖视频 tag 结构（关键）

逐个 tag 解析 NALU（4 字节长度前缀），并解析 HEVC slice header 的
`first_slice_segment_in_pic_flag`（NAL 头后第一个 bit）与 `slice_pic_parameter_set_id`(ue)：

```
ts=0   type=19 first=1 pps=0 len=77682   ← IDR 第 1 个 slice
ts=0   type=19 first=0 pps=0 len=79978   ← IDR 第 2 个 slice
ts=0   type=1  first=1 pps=0 len=2803    ← P 帧第 1 个 slice
ts=40  type=1  first=0 pps=0 len=19147   ← 同一 P 帧第 2 个 slice（时间戳已 +40ms）
ts=40  type=1  first=1 pps=0 len=1535    ← 下一帧第 1 个 slice（时间戳与上一行重复）
ts=80  type=1  first=0 pps=0 len=16357
...
first-slice 分布: { "1": 39, "0": 38 }    pps id 全部 = 0
统计: 264 个视频 tag / 132 个图像（每个图像 2 个 tag）
```

得到两个直接结论：

1. **一个图像 = 2 个 FLV tag**，每个 tag 只含 1 个 VCL slice（`first=1` 后紧跟 `first=0`）；
2. 同一图像两个 slice 的 DTS 相差 40 ms，而且 `slice2(第 N 帧)` 的 DTS 与 `slice1(第 N+1 帧)` **完全相同**（DTS 跨帧重复）。

> 该摄像机每帧编码为 2 个 slice —— 这是"其它流正常、只有这路异常"的分水岭，
> 其它流的每帧只有 1 个 slice，1 tag 恰好就是一个完整 AU。

### 2.4 用 ffmpeg 复现解码问题

```
ffmpeg -v warning -i ws.flv -f null -
```

```
[hevc] PPS changed between slices.
[hevc] Error parsing NAL unit #4.
...
```

统计一次 6 秒抓流：

| 告警 | 次数 |
|---|---|
| `PPS changed between slices` | **137** |
| `Error parsing NAL unit` | **137** |

（另一次 5 秒抓流 132 个图像，ffmpeg 仍能输出 132 帧 —— 因为每帧的首 slice 是有效的，
但每帧都缺第 2 个 slice，且告警次数与帧数同量级。）

libavcodec `hevcdec.c` 中该告警的条件是"非首 slice 到来时，当前图像的 PPS 已不是 `pps_list[pps_id]`"，
而 `Error parsing NAL unit #4` 指向的正是 tag 里第 5 个 NALU（即第 2 个 slice），
说明**每帧的第二个 slice 都被当成"独立的一帧/无效 NAL"处理**。

### 2.5 对照 mpegts.js 源码确认表现

`src/demux/flv-demuxer.js`：

```js
_parseVideoData(...) {
    const isExHeader = (spec & 0b10000000) !== 0;
    const frameType  = (spec & 0b01110000) >>> 4;
    if (isExHeader && fourcc === 'hvc1') {
        this._parseEnhancedHEVCVideoPacket(..., frameType, packetType);  // packetType 3 = CodedFramesX
    }
}

_parseHEVCVideoData(arrayBuffer, dataOffset, dataSize, tagTimestamp, ..., frameType, cts) {
    const units = []; let length = 0;
    while (offset < dataSize) { ... units.push({type: unitType, data: data}) ... }
    const hevcSample = {
        units: units,
        length: length,
        isKeyframe: keyframe,              // frameType === 1 或含 IRAP NAL
        dts: this._timestampBase + tagTimestamp,
        cts, pts: dts + cts
    };
    track.samples.push(hevcSample);        // ★ 一个 FLV tag = 一个 MP4 sample
}
```

即 **1 个 FLV 视频 tag = 1 个 MP4 sample = 解码器视角的 1 帧**。
本路流一个图像 2 个 tag → 2 个 sample，每个 sample 只有半张图：

- 第 1 个 sample（首 slice）只能解出图像的一部分，其余 CTU 没有数据 → **绿屏**；
- 第 2 个 sample 是"没有首 slice 的延续帧"，且 DTS 与下一帧首 slice 相同
  → `mp4-remuxer` 的 `dtsCorrection` 强制错开时间戳，时间轴逐渐崩塌 → **播放一会儿就卡/绿**；
- IDR 帧的两个 tag 各自带一份 VPS/SPS/PPS，图像中间重复下发参数集
  → `PPS changed between slices`，参考帧链断裂。

### 2.6 浏览器侧对照实验（证明"数据不是空的"）

本机用 mpegts.js 1.8.0 播放：

| 实验对象 | 结果 |
|---|---|
| 该路 WS-FLV | 能解析出 `hvc1.1.1.L150.B0 / 2688×1520` 元信息；MSE 报 `PIPELINE_ERROR_DECODE: video decode error!`，`currentTime=0` |
| 基线 H.264 FLV（本地生成，仅视频） | 正常播放（`readyState=4`，`currentTime` 递增） |

说明播放器能收、能解析、能进 MSE；失败在解码/帧结构层面。
（注：本机 Chromium 无 HEVC 硬解、且不含 AAC 专有解码器，因此"本机无法播 HEVC"不作为用户环境的依据，
仅用于证明"数据链路正常、容器可解析"。）

### 2.7 反向验证：只改封装结构就恢复正常

把抓到的同一批 NALU 按"**一个图像一个 tag + 同一 AU 内去重 VPS/SPS/PPS + 写入关键帧标记**"重新封装，
码流内容一个字节都不改：

```
pictures 112  keyframes 3  bytes 2424780
ffprobe → codec_name=hevc  nb_read_frames=112        # 零告警、零丢帧
```

**结论：H.265 码流本身没问题，是封装把 access unit 切碎了。**

---

## 三、问题原因

### 3.1 根因链路（主因）

```
摄像机 PS 流：一帧 = [VPS,SPS,PPS,slice1] + [VPS,SPS,PPS,slice2]（两个 PES，PTS 相差 40ms）
        │
        ▼  github.com/qdwl/mpegps  ps-demuxer.go  demuxH26x()
           每找到一个 start code 就回调一个 NAL：
           psdemuxer.OnFrame(streamBuf[start:end], cid, stream.pts/90, ...)
        │
        ▼  internal/protocols/gb28181/to_stream.go（修复前）
           一个 NAL 调一次 WriteUnit：unit.H265.AU 实际只有 1 个 NALU，且 PTS 来自不同 PES
        │
        ▼  internal/formatprocessor/h265.go  remuxAccessUnit()
           对"包含 IDR 的 unit"都会前置一份 VPS/SPS/PPS
           → IDR 的两个 slice 各带一份参数集（图像中间重复参数集）
        │
        ▼  internal/protocols/flv/from_stream.go:184 → writer.go:178 WriteH265()
           一个 unit 输出一个 FLV tag
        │
        ▼  internal/servers/flv/http_server.go（WS/HTTP-FLV 均原样下发）
        │
        ▼  mpegts.js：1 tag = 1 MP4 sample
           → 半帧样本 / 跨帧重复 DTS / 参数集重复 → 绿屏、卡死
```

**一句话**：`unit.H265.AU` 的语义是"一个完整 access unit"，但 GB28181 通路实际塞进去的是"一个 slice"，
下游所有按 unit 出帧的协议（FLV、HLS/fMP4、WebRTC、RTSP）都会受影响。

### 3.2 次要问题

| # | 问题 | 影响 |
|---|---|---|
| 1 | Enhanced-FLV 视频 tag 首字节恒为 `0x83`，`frameType` 位永远是 0（规范：1=关键帧、2=非关键帧） | mpegts.js 靠 IRAP NAL 兜底还能识别；flv.js / VLC / 工具链 / seek 会认为"没有关键帧" |
| 2 | 音频 tag 时间戳为负 → 按 uint32 写出后回绕成 `4294951xxx` ms；22 秒采样中音频始终比视频落后约 **16.4 秒**，第 16 秒才跳回 0 | 音画严重不同步；`_dtsBase = min(audio, video) = 0`，音频被排到 49 天后；时间戳非单调，MSE 容易出错 |
| 3 | 音频为 **8 kHz** AAC-LC | 部分平台/播放器解码器不接受 8 kHz（需要转码才能彻底解决） |
| 4 | WS 处理协程每次循环调用两次 `ReadMessage()`，并在读协程里用 `WriteMessage` 回 Pong | 吞掉一条上行消息；`WriteMessage` 与写协程并发，gorilla/websocket 不允许 |
| 5 | hvcC 的 `general_constraint_indicator` 取错 SPS 偏移（`sps[7..12]`，应为 `sps[8..13]`） | 只影响元信息，播放器不依赖，本次未改 |

### 3.3 音频负时间戳的来源

`internal/protocols/gb28181/conn.go` 原来只用一个全局 `timebase`（取第一帧的 PTS）：

```go
if c.timebase == 0 { c.timebase = int64(pts) }
ts = time.Duration(pts - uint64(c.timebase))   // uint64 无符号相减
```

该设备音频 PES 的 PTS 比视频小约 16 秒（两路 PTS 基准不同，但数据是同时到达的），
相减下溢成负数，再被 FLV 当 uint32 写出 → `4294951xxx`。

---

## 四、修复方法与内容

### 4.1 新增：NAL → 完整 access unit 聚合（`internal/protocols/gb28181/au.go`）

```go
type auAggregator struct {
    h265         bool
    onAccessUnit func(pts time.Duration, au h264.AnnexB)

    au       h264.AnnexB
    auPTS    time.Duration
    hasFirst bool
}

func (a *auAggregator) push(nalu []byte, pts time.Duration) {
    typ := naluType(nalu, a.h265)
    if isVCL(typ, a.h265) {
        if isFirstSlice(nalu, typ, a.h265) {
            if a.hasFirst && len(a.au) != 0 { a.emit() }   // 上一张图结束
        } else if !a.hasFirst {
            a.au = a.au[:0]; return                        // 半帧数据，丢弃
        }
    } else if isAccessUnitDelimiter(typ, a.h265) && a.hasFirst {
        a.emit()
    }
    if len(a.au) == 0 { a.auPTS = pts; a.hasFirst = false }
    if isParameterSet(typ, a.h265) && containsNALU(a.au, nalu) { return } // AU 内去重
    a.au = append(a.au, append([]byte(nil), nalu...))      // 拷贝，避免 PS buffer 复用
    ...
}
```

要点：

- **图像边界**：H.265 用 `first_slice_segment_in_pic_flag`（NAL 头后第 1 bit）；H.264 解析 `first_mb_in_slice`(ue) 是否为 0；
- **AU 内去重** VPS/SPS/PPS（按字节比较），避免参数集夹在图像中间；
- **PTS 取首 slice 的 PTS**，丢弃后续 slice 那个 "+40ms" 的错误时间戳；
- **NALU 做拷贝**：PS 解封装的 buffer 会被复用，避免下游拿到脏数据；
- 从"半帧"开始的数据直接丢弃，避免把截断图像下发出去；
- AUD 触发 AU 结束；另有 `maxNALUsPerAccessUnit` 兜底防止异常流导致内存增长。

### 4.2 GB28181 出口改为按 AU 下发（`internal/protocols/gb28181/to_stream.go`）

```go
agg := &auAggregator{
    h265: true,
    onAccessUnit: func(pts time.Duration, au h264.AnnexB) {
        (*stream).WriteUnit(medi, medi.Formats[0], &unit.H265{
            Base: unit.Base{
                NTP: time.Now(),
                PTS: durationToTimestamp(pts*time.Millisecond, medi.Formats[0].ClockRate()),
            },
            AU: au,
        })
    },
}

conn.onFrameFunc(track.StreamType, func(pts time.Duration, data []byte) {
    var dec h264.AnnexB
    if err := dec.Unmarshal(data); err != nil { return }
    for _, nalu := range dec { agg.push(nalu, pts) }
})
```

H.264 分支做同样处理（`h265: false`，产出 `unit.H264`）。
下游 `formatprocessor/remuxAccessUnit()` 因此每个 keyframe AU 只会前置一次参数集，重复参数集问题一并消失。

### 4.3 PTS 基准按轨道计算（`internal/protocols/gb28181/conn.go`）

```go
} else {
    // 每路轨道有各自的 PTS 基准，音视频基准不同会产生负时间戳
    key := uint8(cid)
    base, ok := c.trackTimebases[key]
    if !ok { base = int64(pts); c.trackTimebases[key] = base }

    ts = time.Duration(int64(pts) - base)   // 用有符号减法
    if ts < 0 { ts = 0 }
}
```

`playback`（回放）分支仍沿用原来的墙钟时间基准，行为不变。

### 4.4 Enhanced-FLV 写入关键帧标记

- `internal/protocols/flv/video.go`：新增 `FrameTypeKeyFrame = 1` / `FrameTypeInterFrame = 2` 与 `frameType()`；
- `video_ex_frames_x.go` / `video_ex_coded_frames.go`：新增 `IsKeyFrame` 字段，首字节改为
  `0b10000000 | frameType(...) << 4 | packetType`（关键帧 `0x93`/`0x91`，非关键帧 `0xA3`/`0xA1`）；
- `writer.go` 的 `WriteH265()`：`IsKeyFrame: h265.IsRandomAccess(au)`。

### 4.5 WS 处理协程修复（`internal/servers/flv/http_server.go`）

- 去掉每轮多余的 `ReadMessage()`（原来第二次读会阻塞/吞消息）；
- 收到 Ping 时改用并发安全的 `conn.WriteControl(websocket.PongMessage, ...)`；
- 客户端正常关闭改记 Info 日志。

---

## 五、验证

### 5.1 单元测试（新增）

- `internal/protocols/gb28181/au_test.go`：H.265 双 slice 聚合、PTS 取首 slice、参数集去重、
  半帧丢弃、NALU 拷贝语义、AUD 断帧、H.264 `first_mb_in_slice` 解析；
- `internal/protocols/flv/video_ex_test.go`：CodedFrames / CodedFramesX 首字节与关键帧标记。

```bash
go test -count=1 ./internal/protocols/gb28181/ ./internal/protocols/flv/ \
              ./internal/formatprocessor/ ./internal/servers/flv/
# ok / ok / ok
go build ./...
# 通过
```

### 5.2 用真实抓流做端到端对比

把该路抓到的原始 NALU 走**修改后的** `auAggregator` + `flv.Writer` 重新封装，再用 ffmpeg 解码对比：

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `PPS changed between slices` | **137** 次 | **2** 次 |
| `Error parsing NAL unit` | **137** 次 | **2** 次 |
| 每帧 tag 数 | 2（每个 tag 1 个 slice） | 1（含全部 slice） |
| 视频 tag 首字节 | 全部 `0x83`（frameType=0） | IDR `0x93`，其余 `0xA3` |
| DTS 间隔 | 0/40 交错、跨帧重复 | 40ms 均匀递增 |
| 每帧数据完整性 | 只有第 1 个 slice（其余 CTU 无数据 → 绿） | 两个 slice 完整 |
| 解出帧数（同一段 6 秒抓流，137 个图像） | 每帧只能解出首 slice，画面残缺；告警 137 次 | **135 帧完整解码**，告警 2 次 |

修复后仍残留的 2 帧错误来自源端 UDP 丢包（slice 数据本身不完整），与封装无关。

### 5.3 抓流复核命令

```bash
# 1) 看编码与封装
ffprobe -v error -show_streams "http://<host>:59008/xxx.flv?id=..."
#    → hevc (Main) 2688x1520 25fps + aac 8000Hz stereo

# 2) 检查 tag 数与首字节（每帧应只有 1 个 0x93/0xA3 视频 tag）
ffprobe -v warning -count_frames -show_entries stream=nb_read_frames -of default=nw=1 <file.flv>

# 3) 浏览器是否支持 H.265 走 MSE
MediaSource.isTypeSupported('video/mp4; codecs="hvc1.1.6.L150.B0"')
```

---

## 六、影响面

| 协议/模块 | 影响 |
|---|---|
| WS-FLV / HTTP-FLV | 直接修复：一图一 tag、关键帧标记正确、DTS 均匀 |
| HLS(fMP4) / WebRTC / RTSP | 同样受益：unit 恢复为完整 AU，不再是"半帧一帧" |
| GB28181 音视频时间戳 | 各轨道独立基准，不再出现 `4294951xxx` |
| 延迟 | 聚合需要等下一帧首 slice 才下发，**增加约 1 帧（40ms）封装延迟** |

---

## 七、遗留问题与后续建议

1. **源端丢包**：仍有个别帧 slice 不完整（UDP 传输），建议设备侧改用 TCP 收流；
2. **8 kHz AAC**：兼容性较差，如需彻底解决需在服务端转码到 16k/44.1k（代价较高，本次未做）；
3. **hvcC `general_constraint_indicator` 偏移**（`internal/protocols/flv/writer.go` 中 `sps[7..12]`）：纯元信息，暂留；
4. **SPS/PPS 中途变化**（如分辨率切换）：FLV 只在连接建立时下发一次序列头，属于既有设计限制，未在本次范围内；
5. 若后续要让浏览器普遍能播，最稳妥的方案仍是**设备出 H.264 码流**（主/子码流切换）或服务端 H.265→H.264 转码。

---

## 八、提交记录

| 分支 | 提交 | 说明 |
|---|---|---|
| `pro` | `808e2612` | 修复 WS-FLV 播放绿屏/无法播放 |
| `dev` | `129a7185` | 同上（cherry-pick，内容与 `pro` 完全一致） |

涉及文件：

```
internal/protocols/gb28181/au.go                     (新增)
internal/protocols/gb28181/au_test.go                (新增)
internal/protocols/gb28181/conn.go
internal/protocols/gb28181/to_stream.go
internal/protocols/flv/video.go
internal/protocols/flv/video_ex_frames_x.go
internal/protocols/flv/video_ex_coded_frames.go
internal/protocols/flv/writer.go
internal/protocols/flv/video_ex_test.go              (新增)
internal/servers/flv/http_server.go
```
