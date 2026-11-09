#!/usr/bin/env python3
import argparse
import http.client
import json
import pathlib
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

parser = argparse.ArgumentParser()
parser.add_argument('--port', type=int, default=14041)
parser.add_argument('--upstream-port', type=int, default=14040)
parser.add_argument('--out', type=pathlib.Path, required=True)
a = parser.parse_args()
a.out.mkdir(parents=True, exist_ok=True)
lock = threading.Lock()
sequence = 0

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        global sequence
        body = self.rfile.read(int(self.headers['Content-Length']))
        with lock:
            sequence += 1
            n = sequence
        record = {'id': n, 'time': time.time(), 'path': self.path,
                  'bytes': len(body), 'content_type': self.headers.get('Content-Type'),
                  'content_encoding': self.headers.get('Content-Encoding')}
        connection = http.client.HTTPConnection('127.0.0.1', a.upstream_port, timeout=30)
        try:
            connection.request('POST', self.path, body, {'Content-Type': self.headers['Content-Type'],
                               'Content-Length': str(len(body)), **({'Content-Encoding': self.headers['Content-Encoding']} if self.headers.get('Content-Encoding') else {})})
            response = connection.getresponse()
            data = response.read()
            record['status'] = response.status
            self.send_response(response.status)
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except Exception as error:
            record['error'] = str(error)
            self.send_error(502)
        finally:
            connection.close()
            if self.path.startswith('/v1development/profiles'):
                (a.out / f'{n:05d}.otlp').write_bytes(body)
            with lock, (a.out / 'uploads.jsonl').open('a') as f:
                f.write(json.dumps(record) + '\n')

    def log_message(self, *_):
        pass

ThreadingHTTPServer(('127.0.0.1', a.port), Handler).serve_forever()
