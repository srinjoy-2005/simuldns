package main

import "dns/internal/wire"

type Flags = wire.Flags
type Header = wire.Header
type Question = wire.Question
type ResourceRecord = wire.ResourceRecord
type Message = wire.Message

var ParseMessage = wire.ParseMessage
var readName = wire.ReadName
var appendName = wire.AppendName
