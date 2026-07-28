package bookstatuspolicy

import "time"

const (
	DefaultReconnectInitialBackoff = time.Second
	DefaultReconnectMaxBackoff     = 30 * time.Second
	DefaultDialTimeout             = 5 * time.Second
	DefaultHeartbeatTimeout        = 10 * time.Second
	DefaultPrefetch                = 20
	DefaultQueueMaxLengthBytes     = 64 << 20
)
