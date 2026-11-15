// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

/*
reqlog is an HTTP endpoint that answers every request and shows what came in.

Usage:

	reqlog [-a addr] [-o file] [-s status] [-b body]

Point an integration at it instead of the real service and every request it
sends is printed: method, path, headers and the body, with JSON indented.

	reqlog                              # http://127.0.0.1:8099, answers 200
	reqlog -o sent.jsonl                # also keep every request in a file
	reqlog -s 500                       # see how the sender retries
	reqlog -a :9000 -b 'OK'             # another port, a plain-text answer

The answer is the same for every request and the path does not matter, so the
sender's URL can keep its path and only its host changes. It is the -s status
with the -b body, sent as JSON when the body parses as JSON and as plain text
otherwise. The default is 200 with {"ok":true}.

# The log file

With -o every request is appended to the file as one JSON object per line:
its sequence number, time, remote address, method, host, URL, headers and the
body as a string. The file is opened for appending, so a restart continues it
rather than replacing it.

The body is kept as sent, byte order mark included, and a JSON body that 1C
writes usually starts with one. The terminal copy drops the mark before
indenting, since json.Indent refuses it; with jq, drop it yourself:

	jq -r '.body | ltrimstr("\ufeff") | fromjson | .order_id' sent.jsonl

# Listening

The default address is the loopback interface. A sink that accepts anything
and answers 200 is not something to leave reachable from the network by
accident; pass -a :8099 to listen on every interface.

Ctrl+C stops it after the requests in flight are answered.
*/
package main
