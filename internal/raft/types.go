package raft

type CommandType int

const (
	CmdPut CommandType = iota
	CmdDelete
	CmdConfigChange // Configuration change (add/remove node)
)

type Command struct {
	Op    CommandType
	Key   []byte
	Value []byte
}

// ConfigChange represents a cluster membership change
type ConfigChange struct {
	Type   ConfigChangeType `json:"type"`    // AddNode or RemoveNode
	NodeID NodeID           `json:"node_id"` // Node to add or remove
}

type ConfigChangeType int

const (
	ConfigAddNode ConfigChangeType = iota
	ConfigRemoveNode
)

type LogEntry struct {
	Index int
	Term  int
	Data  []byte
}
