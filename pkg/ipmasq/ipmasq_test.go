// Copyright 2020 Authors of Cilium
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

// +build !privileged_tests

package ipmasq

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"testing"
	"time"

	"gopkg.in/check.v1"

	"github.com/cilium/cilium/pkg/lock"
)

func Test(t *testing.T) {
	check.TestingT(t)
}

type ipMasqMapMock struct {
	lock.RWMutex
	cidrs map[string]net.IPNet
}

func (m *ipMasqMapMock) Update(cidr net.IPNet) error {
	m.Lock()
	defer m.Unlock()

	cidrStr := cidr.String()
	if _, ok := m.cidrs[cidrStr]; ok {
		return fmt.Errorf("CIDR already exists: %s", cidrStr)
	}
	m.cidrs[cidrStr] = cidr

	return nil
}

func (m *ipMasqMapMock) Delete(cidr net.IPNet) error {
	m.Lock()
	defer m.Unlock()

	cidrStr := cidr.String()
	if _, ok := m.cidrs[cidrStr]; !ok {
		return fmt.Errorf("CIDR not found: %s", cidrStr)
	}
	delete(m.cidrs, cidrStr)

	return nil
}

func (m *ipMasqMapMock) Dump() ([]net.IPNet, error) {
	m.RLock()
	defer m.RUnlock()

	cidrs := make([]net.IPNet, 0, len(m.cidrs))
	for _, cidr := range m.cidrs {
		cidrs = append(cidrs, cidr)
	}

	return cidrs, nil
}

func (m *ipMasqMapMock) dumpToSet() map[string]struct{} {
	m.RLock()
	defer m.RUnlock()

	cidrs := make(map[string]struct{}, len(m.cidrs))
	for cidrStr := range m.cidrs {
		cidrs[cidrStr] = struct{}{}
	}

	return cidrs
}

type IPMasqTestSuite struct {
	ipMasqMap   *ipMasqMapMock
	ipMasqAgent *IPMasqAgent
	configFile  *os.File
}

var _ = check.Suite(&IPMasqTestSuite{})

func (i *IPMasqTestSuite) SetUpTest(c *check.C) {
	i.ipMasqMap = &ipMasqMapMock{cidrs: map[string]net.IPNet{}}

	configFile, err := ioutil.TempFile("", "ipmasq-test")
	c.Assert(err, check.IsNil)
	i.configFile = configFile

	agent, err := newIPMasqAgent(configFile.Name(), i.ipMasqMap)
	c.Assert(err, check.IsNil)
	i.ipMasqAgent = agent
	i.ipMasqAgent.Start()
}

func (i *IPMasqTestSuite) TearDownTest(c *check.C) {
	i.ipMasqAgent.Stop()
	os.Remove(i.configFile.Name())
}

func (i *IPMasqTestSuite) TestUpdate(c *check.C) {
	_, err := i.configFile.WriteString("nonMasqueradeCIDRs:\n- 1.1.1.1/32\n- 2.2.2.2/16")
	c.Assert(err, check.IsNil)
	time.Sleep(300 * time.Millisecond)

	ipnets := i.ipMasqMap.dumpToSet()
	c.Assert(len(ipnets), check.Equals, 2)
	_, ok := ipnets["1.1.1.1/32"]
	c.Assert(ok, check.Equals, true)
	_, ok = ipnets["2.2.0.0/16"]
	c.Assert(ok, check.Equals, true)

	// Write new config
	_, err = i.configFile.Seek(0, 0)
	c.Assert(err, check.IsNil)
	_, err = i.configFile.WriteString("nonMasqueradeCIDRs:\n- 8.8.0.0/16\n- 2.2.2.2/16")
	c.Assert(err, check.IsNil)
	time.Sleep(300 * time.Millisecond)

	ipnets = i.ipMasqMap.dumpToSet()
	c.Assert(len(ipnets), check.Equals, 2)
	_, ok = ipnets["8.8.0.0/16"]
	c.Assert(ok, check.Equals, true)
	_, ok = ipnets["2.2.0.0/16"]
	c.Assert(ok, check.Equals, true)

	// Write new config in JSON
	_, err = i.configFile.Seek(0, 0)
	c.Assert(err, check.IsNil)
	_, err = i.configFile.WriteString(`{"nonMasqueradeCIDRs": ["8.8.0.0/16", "1.1.2.3/16"]}`)
	c.Assert(err, check.IsNil)
	time.Sleep(300 * time.Millisecond)

	ipnets = i.ipMasqMap.dumpToSet()
	c.Assert(len(ipnets), check.Equals, 2)
	_, ok = ipnets["8.8.0.0/16"]
	c.Assert(ok, check.Equals, true)
	_, ok = ipnets["1.1.0.0/16"]
	c.Assert(ok, check.Equals, true)

	// Delete file, should remove the CIDRs
	err = os.Remove(i.configFile.Name())
	c.Assert(err, check.IsNil)
	err = i.configFile.Close()
	c.Assert(err, check.IsNil)
	time.Sleep(300 * time.Millisecond)
	ipnets = i.ipMasqMap.dumpToSet()
	c.Assert(len(ipnets), check.Equals, 0)
}

func (i *IPMasqTestSuite) TestRestore(c *check.C) {
	// Check that stale entry is removed from the map after restore
	i.ipMasqAgent.Stop()

	_, cidr, _ := net.ParseCIDR("3.3.3.0/24")
	i.ipMasqMap.cidrs[cidr.String()] = *cidr
	_, cidr, _ = net.ParseCIDR("4.4.0.0/16")
	i.ipMasqMap.cidrs[cidr.String()] = *cidr

	_, err := i.configFile.WriteString("nonMasqueradeCIDRs:\n- 4.4.0.0/16")
	c.Assert(err, check.IsNil)

	i.ipMasqAgent, err = newIPMasqAgent(i.configFile.Name(), i.ipMasqMap)
	c.Assert(err, check.IsNil)
	i.ipMasqAgent.Start()
	time.Sleep(300 * time.Millisecond)

	ipnets := i.ipMasqMap.dumpToSet()
	c.Assert(len(ipnets), check.Equals, 1)
	_, ok := ipnets["4.4.0.0/16"]
	c.Assert(ok, check.Equals, true)

	// Now stop the goroutine, and also remove the maps. It should bootstrap from
	// the config
	i.ipMasqAgent.Stop()
	i.ipMasqMap = &ipMasqMapMock{cidrs: map[string]net.IPNet{}}
	i.ipMasqAgent.ipMasqMap = i.ipMasqMap
	_, err = i.configFile.Seek(0, 0)
	c.Assert(err, check.IsNil)
	_, err = i.configFile.WriteString("nonMasqueradeCIDRs:\n- 3.3.0.0/16")
	c.Assert(err, check.IsNil)
	i.ipMasqAgent, err = newIPMasqAgent(i.configFile.Name(), i.ipMasqMap)
	c.Assert(err, check.IsNil)
	i.ipMasqAgent.Start()

	ipnets = i.ipMasqMap.dumpToSet()
	c.Assert(len(ipnets), check.Equals, 1)
	_, ok = ipnets["3.3.0.0/16"]
	c.Assert(ok, check.Equals, true)
}