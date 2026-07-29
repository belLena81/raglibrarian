package defaults

const (
	ParserSandboxMemoryBytesMax = int64(8 << 30)
	ParserSandboxMemoryBytesMin = int64(1536 << 20)
	CommandStderrBytes          = 8 << 10

	EPUBMaximumEntries       = 2048
	EPUBMaximumSpineItems    = uint32(500)
	EPUBMaximumEntryBytes    = int64(32 << 20)
	EPUBMaximumExpandedBytes = int64(256 << 20)
	EPUBMaximumTextBytes     = int64(128 << 20)

	EPUBMaximumEntriesLimit       = 8192
	EPUBMaximumSpineItemsLimit    = uint32(5000)
	EPUBMaximumEntryBytesLimit    = int64(256 << 20)
	EPUBMaximumExpandedBytesLimit = int64(2 << 30)
	EPUBMaximumTextBytesLimit     = int64(1 << 30)
)
