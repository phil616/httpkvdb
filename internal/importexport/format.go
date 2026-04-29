package importexport

import "errors"

var (
	Magic            = [8]byte{'K', 'V', 'H', 'T', 'T', 'P', '0', '1'}
	ErrInvalidFormat = errors.New("invalid export format")
	ErrChecksum      = errors.New("checksum mismatch")
)

const FormatVersion uint32 = 1

const (
	valueTypeString uint8 = 1
	valueTypeJSON   uint8 = 2
	valueTypeBinary uint8 = 3
)
