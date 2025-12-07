package raft

type CommandType int

const (
	CmdPut CommandType = iota
	CmdDelete
)

type Command struct {
	Op    CommandType
	Key   []byte
	Value []byte
}

type LogEntry struct {
	Index int
	Term  int
	Data  []byte
}
