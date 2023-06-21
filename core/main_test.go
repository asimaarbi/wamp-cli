/*
*
* Copyright 2021-2022 Simple Things Inc.
*
* Permission is hereby granted, free of charge, to any person obtaining a copy
* of this software and associated documentation files (the "Software"), to deal
* in the Software without restriction, including without limitation the rights
* to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
* copies of the Software, and to permit persons to whom the Software is
* furnished to do so, subject to the following conditions:
*
* The above copyright notice and this permission notice shall be included in all
* copies or substantial portions of the Software.
*
* THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
* IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
* FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
* AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
* LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
* OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
* SOFTWARE.
*
 */

package core_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gammazero/nexus/v3/client"
	"github.com/gammazero/nexus/v3/wamp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/s-things/wick/core"
	"github.com/s-things/wick/internal/testutil"
)

const (
	testProcedure = "wick.test.procedure"
	testTopic     = "wick.test.topic"
	repeatCount   = 1000
	repeatPublish = 1000
	delay         = 1000
)

func TestRegisterDelay(t *testing.T) {
	rout := testutil.NewTestRouter(t, testutil.TestRealm)
	session := testutil.NewTestClient(t, rout)

	go func() {
		err := core.Register(session, testProcedure, core.RegisterOption{Delay: delay})
		assert.NoError(t, err, fmt.Sprintf("error in registering procedure: %s\n", err))
	}()

	err := session.Unregister(testProcedure)
	assert.Error(t, err, "procedure should register after 1 second")

	time.Sleep(1100 * time.Millisecond)
	err = session.Unregister(testProcedure)
	assert.NoError(t, err, "procedure not even register after delay")
}

func TestRegisterInvokeCount(t *testing.T) {
	invokeCount := 2
	sessionRegister, sessionCall := testutil.ConnectedTestClients(t)

	err := core.Register(sessionRegister, testProcedure, core.RegisterOption{InvokeCount: invokeCount})
	require.NoError(t, err, fmt.Sprintf("error in registering procedure: %s\n", err))

	for i := 0; i < invokeCount; i++ {
		_, err = sessionCall.Call(context.Background(), testProcedure, nil, nil, nil, nil)
		require.NoError(t, err, fmt.Sprintf("error in calling procedure: %s\n", err))
	}
	err = sessionRegister.Unregister(testProcedure)
	require.Error(t, err, "procedure not unregister after invoke-count")
}

func TestRegisterOnInvocationCmd(t *testing.T) {
	sessionRegister, sessionCall := testutil.ConnectedTestClients(t)

	err := core.Register(sessionRegister, testProcedure, core.RegisterOption{Command: "pwd"})
	require.NoError(t, err, fmt.Sprintf("error in registering procedure: %s\n", err))

	result, err := sessionCall.Call(context.Background(), testProcedure, nil, nil, nil, nil)
	require.NoError(t, err, fmt.Sprintf("error in calling procedure: %s\n", err))

	out, _, _ := core.ShellOut("pwd")
	require.Equal(t, out, result.Arguments[0], "invalid call results")
}

func mockStdout(t *testing.T, mockStdout *os.File) {
	oldStdout := os.Stdout
	t.Cleanup(func() { os.Stdout = oldStdout })
	os.Stdout = mockStdout
}

func checkOutput(t *testing.T, session *client.Client, args []string, kwargs map[string]string,
	expectedOutput string, opts core.CallOptions) {
	rescueStdout := os.Stdout
	r, w, err := os.Pipe()
	assert.NoError(t, err)
	os.Stdout = w

	err = core.Call(session, testProcedure, args, kwargs, opts)
	require.NoError(t, err)
	w.Close()
	out, err := io.ReadAll(r)
	assert.NoError(t, err)
	os.Stdout = rescueStdout
	assert.Equal(t, expectedOutput, string(out))
}

func TestCallDelayRepeatConcurrency(t *testing.T) {
	sessionRegister, sessionCall := testutil.ConnectedTestClients(t)

	var m sync.Mutex
	iterator := 0
	invocationHandler := func(ctx context.Context, inv *wamp.Invocation) client.InvokeResult {
		m.Lock()
		iterator++
		m.Unlock()
		return client.InvokeResult{Args: inv.Arguments, Kwargs: inv.ArgumentsKw}
	}

	err := sessionRegister.Register(testProcedure, invocationHandler, nil)
	require.NoError(t, err, fmt.Sprintf("error in registering procedure: %s\n", err))
	t.Cleanup(func() { sessionRegister.Unregister(testProcedure) })

	t.Run("TestCallDelay", func(t *testing.T) {
		go func() {
			err = core.Call(sessionCall, testProcedure, []string{"Hello", "1"}, nil, core.CallOptions{
				DelayCall: 1000,
			})
			require.NoError(t, err, fmt.Sprintf("error in calling procedure: %s\n", err))
		}()
		m.Lock()
		iter := iterator
		m.Unlock()
		require.Equal(t, 0, iter, "procedure called without delay")
		time.Sleep(1100 * time.Millisecond)

		m.Lock()
		iter = iterator
		m.Unlock()

		require.Equal(t, 1, iter, "procedure not even called after delay")
		iterator = 0
	})

	t.Run("TestCallRepeat", func(t *testing.T) {
		// to avoid logging of call results
		mockStdout(t, os.NewFile(uintptr(syscall.Stdin), os.DevNull))

		err = core.Call(sessionCall, testProcedure, []string{"Hello", "1"}, nil, core.CallOptions{
			RepeatCount: repeatCount,
		})
		require.NoError(t, err, fmt.Sprintf("error in calling procedure: %s\n", err))
		require.Eventually(t, func() bool {
			m.Lock()
			iter := iterator
			m.Unlock()
			require.Equal(t, repeatCount, iter, "procedure not correctly called repeatedly")
			return true
		}, 1*time.Second, 50*time.Millisecond)
	})

	t.Run("TestBackwardsCompatibility", func(t *testing.T) {
		// output with args only
		checkOutput(t, sessionCall, []string{"Hello", "1"}, nil, `[
    "Hello",
    1
]
`, core.CallOptions{})
		// output with kwargs only
		checkOutput(t, sessionCall, nil, map[string]string{
			"foo": "bar",
			"num": "1",
		}, `kwargs:
{
    "foo": "bar",
    "num": 1
}
`, core.CallOptions{})
		// output with args and kwargs
		checkOutput(t, sessionCall, []string{"Hello", "1"}, map[string]string{
			"foo": "bar",
			"num": "1",
		}, `args:
[
    "Hello",
    1
]
kwargs:
{
    "foo": "bar",
    "num": 1
}
`, core.CallOptions{})
		// output with no args and kwargs
		checkOutput(t, sessionCall, nil, nil, "", core.CallOptions{})
	})
}

func TestCallJsonOutput(t *testing.T) {
	callee, caller := testutil.ConnectedTestClients(t)
	err := callee.Register(testProcedure, func(ctx context.Context, invocation *wamp.Invocation) client.InvokeResult {
		return client.InvokeResult{Args: invocation.Arguments, Kwargs: invocation.ArgumentsKw}
	}, nil)
	require.NoError(t, err)

	// test with no args and kwargs
	checkOutput(t, caller, nil, nil, `{
    "args": [],
    "kwargs": {}
}
`, core.CallOptions{JsonOutput: true})

	// test with args only
	checkOutput(t, caller, []string{"abc"}, nil, `{
    "args": [
        "abc"
    ],
    "kwargs": {}
}
`, core.CallOptions{JsonOutput: true})

	// test with kwargs only
	checkOutput(t, caller, nil, map[string]string{
		"abc": "123",
		"xyz": "true",
	}, `{
    "args": [],
    "kwargs": {
        "abc": 123,
        "xyz": true
    }
}
`, core.CallOptions{JsonOutput: true})

	// test with args and kwargs
	checkOutput(t, caller, []string{"abc"}, map[string]string{
		"abc": "123",
		"xyz": "true",
	}, `{
    "args": [
        "abc"
    ],
    "kwargs": {
        "abc": 123,
        "xyz": true
    }
}
`, core.CallOptions{JsonOutput: true})
}

func TestSubscribe(t *testing.T) {
	rout := testutil.NewTestRouter(t, testutil.TestRealm)

	session := testutil.NewTestClient(t, rout)

	err := core.Subscribe(session, testTopic, core.SubscribeOptions{})
	require.NoError(t, err, fmt.Sprintf("error in subscribing: %s\n", err))

	err = session.Unsubscribe(testTopic)
	require.NoError(t, err, fmt.Sprintf("error in subscribing: %s\n", err))
}

func TestPublishDelayRepeatConcurrency(t *testing.T) {
	sessionSubscribe, sessionPublish := testutil.ConnectedTestClients(t)

	var m sync.Mutex
	iterator := 0
	eventHandler := func(event *wamp.Event) {
		m.Lock()
		iterator++
		m.Unlock()
	}

	err := sessionSubscribe.Subscribe(testTopic, eventHandler, nil)
	require.NoError(t, err, fmt.Sprintf("error in subscribing topic: %s\n", err))
	t.Cleanup(func() { sessionSubscribe.Unsubscribe(testTopic) })

	t.Run("TestPublishDelay", func(t *testing.T) {
		go func() {
			err = core.Publish(sessionPublish, testTopic, nil, nil, core.PublishOptions{
				Repeat:      1,
				Delay:       1000,
				Concurrency: 1,
			})
			require.NoError(t, err, fmt.Sprintf("error in publishing: %s\n", err))
		}()
		m.Lock()
		iter := iterator
		m.Unlock()
		require.Equal(t, 0, iter, "topic published without delay")
		time.Sleep(1100 * time.Millisecond)

		m.Lock()
		iter = iterator
		m.Unlock()
		require.Equal(t, 1, iter, "topic not even published after delay")
		iterator = 0
	})

	t.Run("TestPublishRepeat", func(t *testing.T) {
		err = core.Publish(sessionPublish, testTopic, []string{"Hello", "1"}, nil, core.PublishOptions{
			Repeat:      repeatPublish,
			Concurrency: 1,
		})
		require.NoError(t, err, fmt.Sprintf("error in publishing topic: %s\n", err))

		require.Eventually(t, func() bool {
			m.Lock()
			iter := iterator
			m.Unlock()
			require.Equal(t, repeatPublish, iter, "topic not correctly publish repeatedly")
			return true
		}, 1*time.Second, 50*time.Millisecond)
	})
}
