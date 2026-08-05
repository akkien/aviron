# Observability tại các công ty lớn (Uber, Netflix, Google, Meta...)

Ở các công ty như Uber, Netflix, Google, Meta..., **observability không phải là một hệ thống duy nhất**, mà là **một platform gồm hàng chục service** phục vụ nhiều mục đích:

- Monitoring (metrics)
- Logging
- Distributed tracing
- Profiling
- Alerting
- Incident response
- Capacity planning
- Cost analysis
- Security auditing

Một hệ thống lớn có thể ingest **vài TB đến hàng PB dữ liệu observability mỗi ngày**.

---

# 1. Ba loại dữ liệu chính

Hầu như mọi công ty đều thu thập ba loại dữ liệu, thường gọi là **Three Pillars of Observability**:

```text
Application
     │
     ▼
Logs
Metrics
Traces
```

---

# 2. Metrics

Metrics là loại dữ liệu được sử dụng nhiều nhất vì:

- Nhẹ
- Rẻ
- Query rất nhanh

Ví dụ:

```text
http_requests_total

instance=api-1
status=200
endpoint=/login

value=1234567
```

Hoặc:

```text
cpu_usage

instance=node-5

value=62%
```

## Metrics thường bao gồm

### Infrastructure

- CPU
- RAM
- Disk
- Network

### Application

- QPS
- Latency
- Error rate
- Queue length
- Kafka lag
- Redis hit rate
- Database connections

### Business

- Orders/minute
- Payment success
- Active users

## Thu thập như thế nào?

Ví dụ trên Kubernetes:

```text
Pod
 │
 ▼
Prometheus Exporter
 │
 ▼
Prometheus Scrape
 │
 ▼
TSDB
```

Các exporter phổ biến:

- Node Exporter
- Redis Exporter
- Kafka Exporter
- PostgreSQL Exporter
- Envoy Exporter

Ví dụ ứng dụng Go:

```go
prometheus.NewCounter(...)
```

Expose endpoint:

```text
/metrics
```

Prometheus sẽ scrape định kỳ (ví dụ mỗi **15 giây**).

---

# 3. Logs

Logs vẫn cực kỳ quan trọng.

Ví dụ:

```text
User login

user=123
ip=...
latency=20ms
status=200
```

Hoặc:

```text
payment failed

order=...
reason=timeout
```

Thông thường log được ghi dưới dạng JSON:

```json
{
  "level": "error",
  "service": "payment",
  "trace_id": "...",
  "user": "123",
  "msg": "timeout"
}
```

## Thu thập

```text
stdout
   │
   ▼
Fluent Bit
   │
   ▼
Kafka
   │
   ▼
Log Storage
```

Log Storage có thể là:

- Elasticsearch
- ClickHouse
- Loki
- S3

---

# Elasticsearch còn dùng không?

Có.

Tuy nhiên xu hướng đã thay đổi.

## Giai đoạn 2015–2020

ELK gần như là tiêu chuẩn:

```text
ELK

Elasticsearch
Logstash
Kibana
```

## Hiện nay

Nhiều công ty chuyển sang:

- ClickHouse
- Loki
- BigQuery
- S3 + Athena
- Hệ thống nội bộ

### Elasticsearch

**Ưu điểm**

- Full-text search
- Kibana
- Mature ecosystem

**Nhược điểm**

- Tốn RAM
- Storage đắt
- Cluster maintenance khó
- Reindex rất tốn kém
- Mapping issues

Uber từng chia sẻ rằng khi log tăng lên quy mô rất lớn, Elasticsearch trở nên quá tốn kém nên nhiều workload được chuyển sang các hệ thống lưu trữ khác.

---

# 4. Distributed Tracing

Tracing giúp theo dõi toàn bộ đường đi của một request.

Ví dụ:

```text
Gateway
   │
   ▼
Order
   │
   ▼
Payment
   │
   ▼
Inventory
   │
   ▼
Notification
```

Một request sẽ tạo ra:

```text
Trace
 ├── Span
 ├── Span
 ├── Span
 └── Span
```

Mỗi span chứa:

- Duration
- Status
- Attributes
- Service

Ví dụ:

```text
Gateway   20ms
      │
      ▼
Payment 150ms
      │
      ▼
DB      120ms
```

=> Có thể xác định bottleneck nằm ở DB.

## Thu thập

```text
OpenTelemetry SDK
        │
        ▼
OTel Collector
        │
        ▼
Jaeger / Tempo / Zipkin
```

---

# 5. Profiling

Profiling ngày càng phổ biến.

Ví dụ:

- CPU profile
- Memory profile
- Heap
- Mutex
- Block

Các công cụ phổ biến:

- Pyroscope
- Parca
- Grafana Alloy

---

# 6. Kiến trúc tổng thể

```text
                    Applications
                         │
        ┌────────────────┼────────────────┐
        │                │                │
      Logs            Metrics         Traces
        │                │                │
        ▼                ▼                ▼
   Fluent Bit      Prometheus       OpenTelemetry
        │           Scraper          Collector
        └──────────────┬────────────────┘
                       │
                 Kafka / Pulsar
                       │
      ┌────────────────┼─────────────────┐
      │                │                 │
      ▼                ▼                 ▼
 Log Storage     Metric TSDB       Trace Storage
(Loki/ClickHouse)(Mimir/Victoria)(Tempo/Jaeger)
      │                │                 │
      └────────────────┼─────────────────┘
                       ▼
               Grafana / Kibana
                       │
                  Alertmanager
                       │
             PagerDuty / Slack / Email
```

---

# 7. Prometheus có đủ không?

Không.

Prometheus chủ yếu làm:

- Scrape metrics
- Lưu metrics
- PromQL

Prometheus **không phù hợp** để:

- Lưu logs
- Lưu traces
- Lưu metrics nhiều năm

Các công ty lớn thường sử dụng:

```text
Prometheus
      │
      ▼
remote_write
      │
      ▼
Thanos / Mimir / VictoriaMetrics
```

để mở rộng khả năng lưu trữ.

---

# 8. Kafka có vai trò gì?

Kafka thường đóng vai trò **pipeline trung gian**.

```text
Logs
 │
 ▼
Kafka
 │
 ▼
Consumers
```

Một log có thể được nhiều consumer sử dụng:

- Elasticsearch
- S3
- Security
- Machine Learning
- Fraud Detection

Đây là lý do Kafka xuất hiện rất nhiều trong các hệ thống observability.

---

# 9. Netflix vận hành như thế nào?

Netflix chạy hàng nghìn microservice.

Ví dụ một request:

```text
API
 │
 ▼
Recommendation
 │
 ▼
Metadata
 │
 ▼
Video
 │
 ▼
CDN
```

Mỗi service đều:

- Emit metrics
- Emit logs
- Emit traces

### Metrics dùng để theo dõi

- SLO
- Availability
- Latency
- Traffic

### Logs

Điều tra lỗi.

### Traces

Tìm service bị chậm.

Netflix còn nổi tiếng với việc tự phát triển nhiều công cụ vận hành như Atlas (metrics) và các nền tảng quan sát nội bộ trước khi hệ sinh thái Prometheus/OpenTelemetry trở nên phổ biến.

---

# 10. Uber

Uber từng công bố pipeline observability quy mô rất lớn.

```text
Millions of events/sec
         │
         ▼
       Kafka
         │
         ▼
     Consumers
         │
         ▼
      Storage
```

Ví dụ:

- Metrics → M3
- Logs → Kafka → Storage
- Traces → Jaeger

Uber phát triển **M3**, một hệ thống time-series phân tán để xử lý lượng metrics rất lớn thay vì chỉ dựa vào Prometheus đơn lẻ.

---

# 11. Tình huống thực tế: Game bị lag

Giả sử hệ thống:

```text
Gateway
   │
   ▼
Room
   │
   ▼
Game
   │
   ▼
Redis
   │
   ▼
Postgres
```

Người chơi báo:

```text
Lag 2 giây
```

## Bước 1. Metrics

Grafana:

```text
Room latency

10ms
 ↓
500ms
```

Đồng thời:

```text
Redis CPU

95%
```

=> Nghi Redis là bottleneck.

---

## Bước 2. Trace

```text
Gateway      5ms
      │
      ▼
Room        20ms
      │
      ▼
Redis GET 480ms
```

=> Xác nhận Redis chiếm phần lớn thời gian.

---

## Bước 3. Logs

Theo `trace_id`:

```text
Redis timeout

retry
retry
retry
```

=> Có nhiều lần retry.

---

## Bước 4. Infrastructure Metrics

Node Exporter cho thấy:

```text
Disk IO = 100%
```

hoặc

```text
Network saturated
```

=> Redis bị nghẽn tài nguyên.

---

## Bước 5. Khắc phục

- Thêm Redis replica hoặc shard.
- Điều chỉnh timeout/retry.
- Tối ưu key hoặc cache.
- Theo dõi dashboard xác nhận latency trở lại bình thường.

**Tóm lại**

- Metrics → phát hiện vấn đề.
- Traces → khoanh vùng.
- Logs → giải thích nguyên nhân.

---

# 12. Tình huống thực tế: Kafka Consumer bị chậm

Giả sử game ghi sự kiện vào Kafka để lưu xuống database.

## Triệu chứng

- Dashboard online bình thường.
- Dữ liệu thống kê chậm khoảng **20 phút**.

## Điều tra

### Metrics

`consumer_lag` tăng từ vài trăm lên vài triệu message.

### Logs

```
database connection timeout
```

### Trace

Span ghi database mất **2–3 giây** thay vì **20 ms**.

### Infrastructure Metrics

Postgres đạt giới hạn số lượng connection.

## Khắc phục

- Tăng connection pool.
- Scale database.
- Scale consumer sau khi database ổn định.
- Đặt alert cho `consumer_lag`.

---

# Stack Observability hiện đại

| Thành phần | Công nghệ phổ biến |
|------------|--------------------|
| Instrumentation | OpenTelemetry |
| Metrics | Prometheus + Thanos / Mimir / VictoriaMetrics |
| Logs | Fluent Bit → Kafka → Loki hoặc ClickHouse (một số workload vẫn dùng Elasticsearch) |
| Traces | OpenTelemetry Collector → Tempo hoặc Jaeger |
| Profiling | Pyroscope hoặc Parca |
| Dashboard | Grafana |
| Alerting | Alertmanager + PagerDuty / Slack |
| Long-term archive | S3 / GCS / Object Storage |

---

# Kết luận

Các công ty lớn **không dựa vào một công nghệ duy nhất**.

Thông thường:

- **OpenTelemetry** chuẩn hóa telemetry.
- **Prometheus** thu thập metrics.
- **Kafka** làm pipeline truyền dữ liệu.
- **Mimir / VictoriaMetrics** lưu metrics dài hạn.
- **Loki / ClickHouse / Elasticsearch** lưu logs.
- **Tempo / Jaeger** lưu traces.
- **Grafana** trực quan hóa.
- **Alertmanager** gửi cảnh báo.

Toàn bộ hệ thống được thiết kế theo hướng **scale từng thành phần độc lập**, đủ khả năng xử lý từ hàng triệu đến hàng chục triệu sự kiện mỗi giây.