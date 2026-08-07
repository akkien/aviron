
# Golang Profiling với `net/http/pprof`

## `net/http/pprof` là gì?

-   Package chuẩn của Go để thu thập runtime profile (CPU, Heap,
    Goroutine, Mutex, Block, Trace...).
-   Chủ yếu phục vụ **debug** và **profiling**.
-   Không phải là hệ thống lưu trữ profile.

## Cách bật

``` go
import _ "net/http/pprof"
import "net/http"

go func() {
    http.ListenAndServe(":6060", nil)
}()
```

Endpoint:

-   `/debug/pprof/profile`
-   `/debug/pprof/heap`
-   `/debug/pprof/goroutine`
-   `/debug/pprof/mutex`
-   `/debug/pprof/block`
-   `/debug/pprof/trace`

## Xem profile trực tiếp

``` bash
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30
```

`go tool pprof` sẽ lấy profile trong **30 giây kể từ lúc chạy lệnh**,
không phải dữ liệu quá khứ.

## Kiến trúc Local Debug

``` text
Application
      │
      ▼
net/http/pprof
      │
      ▼
go tool pprof
```

Phù hợp để debug khi ứng dụng đang chạy.

**Hạn chế:** không có lịch sử. Nếu CPU spike xảy ra hôm qua thì hôm nay
không thể dùng endpoint này để xem lại.

## Thu profile định kỳ

``` text
Application
      │
      ▼
pprof endpoint
      │
Collector / Cron Job
      │
      ▼
Profile Storage (S3, NAS,...)
      │
      ▼
go tool pprof
```

Collector định kỳ tải profile và lưu lại để phân tích sau.

## Trigger khi có Alert

``` text
Prometheus Alert
      │
      ▼
Collector
      │
GET /debug/pprof/profile
      │
      ▼
Profile Storage
```

Chỉ lưu profile khi hệ thống có dấu hiệu bất thường nhằm tiết kiệm dung
lượng.

## Continuous Profiling (Khuyến nghị cho Production)

``` text
Application
      │
      ▼
Pyroscope SDK / Agent
      │
      ▼
Pyroscope Server
      │
      ▼
Grafana
```

Pyroscope sẽ:

-   thu profile liên tục,
-   lưu lịch sử,
-   hiển thị timeline,
-   flame graph,
-   so sánh profile giữa các thời điểm.

Ví dụ có thể xem:

-   CPU lúc 14:30 hôm qua
-   Heap trước và sau khi deploy
-   Goroutine tăng dần theo thời gian

## Vai trò của Grafana

Grafana không đọc trực tiếp endpoint pprof.

Thông thường:

-   Metrics ← Prometheus
-   Logs ← Loki
-   Traces ← Tempo
-   Profiles ← Pyroscope

## Khi nào dùng cách nào?

  Nhu cầu                Giải pháp
  ---------------------- --------------------------
  Debug local            pprof + go tool pprof
  Muốn lưu profile       Collector/Cron + Storage
  Chỉ lưu khi có sự cố   Alert + Collector
  Production lâu dài     Pyroscope

## Kết luận

-   `net/http/pprof` là công cụ **thu thập profile**, không phải hệ
    thống lưu trữ.
-   `go tool pprof` có thể phân tích profile lấy trực tiếp từ endpoint
    hoặc từ file đã lưu.
-   Nếu muốn điều tra sự cố đã xảy ra trong quá khứ, bạn phải lưu
    profile (collector) hoặc dùng hệ thống continuous profiling như
    **Pyroscope**.
