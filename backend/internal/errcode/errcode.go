package errcode

const (
	MaxHD             = "max_hd"
	TargetSwitching   = "target_switching"
	QueueTimeout      = "queue_timeout"
	NotCtrlOnline     = "not_ctrl_online"
	TSPullFailed      = "ts_pull_failed"
	IDRTimeout        = "idr_timeout"
	MTXPublisherLost  = "mtx_publisher_lost"
	AuthPublishOnly   = "auth_publish_only"
	AuthPublishDenied = "auth_publish_denied"
	AuthRateLimited   = "auth_rate_limited"
	MTXAPIError       = "mtx_api_error"
	SessionDead       = "session_dead"
	SwitchSeqStale    = "switch_seq_stale"
	DeviceDestroyed   = "device_destroyed"
	PageNotVisible    = "page_not_visible"
)
