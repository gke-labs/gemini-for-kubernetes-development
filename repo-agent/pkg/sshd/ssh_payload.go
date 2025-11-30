// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sshd

import (
	"encoding/binary"
	"fmt"
)

// sshPayload helps parse SSH request payloads.
type sshPayload struct {
	b []byte
}

// PopString pops a string from the payload.
func (p *sshPayload) PopString() (string, error) {
	if len(p.b) < 4 {
		return "", fmt.Errorf("payload too short to read string length")
	}
	strLen := binary.BigEndian.Uint32(p.b)
	if uint32(len(p.b)) < 4+strLen {
		return "", fmt.Errorf("payload too short to read string (length %d)", strLen)
	}
	str := string(p.b[4 : 4+strLen])
	p.b = p.b[4+strLen:]
	return str, nil
}

// PopUint32 pops a uint32 from the payload.
func (p *sshPayload) PopUint32() (uint32, error) {
	if len(p.b) < 4 {
		return 0, fmt.Errorf("payload too short to read uint32")
	}
	v := binary.BigEndian.Uint32(p.b)
	p.b = p.b[4:]
	return v, nil
}

// Len returns the length of the remaining payload.
func (p *sshPayload) Len() int {
	return len(p.b)
}
