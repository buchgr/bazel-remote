# Metrics

The following is a list of interesting [promql](https://prometheus.io/docs/prometheus/latest/querying/basics/) metrics that you can use to visualize about the effectiveness and utlization of your remote cache.

### Cache Hit Percentage By Type

```promql
sum by (kind) (rate(bazel_remote_incoming_requests_total{status="hit"}[$__rate_interval]))
/
sum by (kind) (rate(bazel_remote_incoming_requests_total[$__rate_interval]))
* 100
```

### Cache Hit Percentage Overall

```promql
sum(rate(bazel_remote_incoming_requests_total{status="hit"}[$__rate_interval]))
/
sum(rate(bazel_remote_incoming_requests_total[$__rate_interval]))
* 100
```

### Request Rate

```promql
sum(rate(bazel_remote_incoming_requests_total[$__rate_interval]))
```

### Request Duration Quantiles

```promql
histogram_quantile(0.99, sum by(le) (rate(http_request_duration_seconds_bucket{k8s_cluster_name="bazel-remote-cache"}[$__rate_interval]) ))
```

### S3 Cache Hit Percentage Overall

```promql
sum(rate(bazel_remote_s3_cache_hits_total[$__rate_interval])) 
/ 
(sum(rate(bazel_remote_s3_cache_hits_total[$__rate_interval]))
  + 
 sum(rate(bazel_remote_s3_cache_misses_total[$__rate_interval]))
) 
* 100
```