#!/usr/bin/env python3
"""End-to-end smoke test. SMOKE_DATABASE_URL MUST point to an empty disposable database.
No production URL is read, and no real DWS account is contacted.
"""
import base64
import hashlib
import http.cookiejar
import json
import os
import secrets
import signal
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def main():
    database = os.environ.get('SMOKE_DATABASE_URL')
    if not database:
        raise SystemExit('SMOKE_DATABASE_URL must designate an empty disposable database')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    origin = f'http://127.0.0.1:{port}'
    password = secrets.token_urlsafe(24)
    env = dict(os.environ, DATABASE_URL=database, HTTP_ADDR=f'127.0.0.1:{port}',
               PUBLIC_ORIGIN=origin, COOKIE_SECURE='false',
               BOOTSTRAP_ADMIN_USERNAME='smoke_admin', BOOTSTRAP_ADMIN_PASSWORD=password)
    binary = env.get('SMOKE_DWS_BINARY', '')
    if binary:
        env.update(DWS_BINARY=binary, DWS_BINARY_SHA256=hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
                   CREDENTIAL_KEY=base64.b64encode(secrets.token_bytes(32)).decode())
    else:
        env['DWS_BINARY'] = ''
    process = subprocess.Popen([str(ROOT / 'bin/api')], env=env, cwd=ROOT,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    admin_jar = http.cookiejar.CookieJar()
    user_jar = http.cookiejar.CookieJar()

    def request(path, method='GET', body=None, expected=200, jar=admin_jar, csrf=True):
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
        headers = {'Origin': origin}
        if body is not None:
            headers['Content-Type'] = 'application/json'
        if csrf:
            for cookie in jar:
                if cookie.name == 'academic_csrf':
                    headers['X-CSRF-Token'] = cookie.value
        req = urllib.request.Request(origin + path, method=method, headers=headers,
                                     data=json.dumps(body).encode() if body is not None else None)
        try:
            response = opener.open(req, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        data = response.read()
        assert response.status == expected, (method, path, response.status, expected, data)
        if 'application/json' in response.headers.get('Content-Type', '') and data:
            return json.loads(data)
        return data

    try:
        deadline = time.monotonic() + 15
        while True:
            if process.poll() is not None:
                raise AssertionError('API exited before readiness')
            try:
                assert request('/api/v1/health') == {'status': 'ok'}
                break
            except urllib.error.URLError:
                if time.monotonic() > deadline:
                    raise
                time.sleep(0.1)
        page = request('/admin')
        assert b'<html' in page and '用户'.encode() in page
        request('/api/v1/users', expected=401)
        admin = request('/api/v1/auth/login', 'POST', {'username': 'smoke_admin', 'password': password})
        assert admin['role'] == 'admin' and 'password_hash' not in admin
        assert request('/api/v1/auth/me')['id'] == admin['id']
        assert len(request('/api/v1/users?limit=100')['items']) == 1
        user_password = secrets.token_urlsafe(24)
        user = request('/api/v1/users', 'POST', {'username': 'smoke_student', 'display_name': '测试用户', 'password': user_password, 'role': 'user'}, expected=201)
        request('/api/v1/users/' + user['id'], 'PUT', {'display_name': '更新姓名'}, expected=403, csrf=False)
        changed = request('/api/v1/users/' + user['id'], 'PUT', {'display_name': '更新姓名'})
        assert changed['display_name'] == '更新姓名'
        request('/api/v1/auth/login', 'POST', {'username': 'smoke_student', 'password': user_password}, jar=user_jar)
        request('/api/v1/users', expected=403, jar=user_jar)
        request('/api/v1/users/' + admin['id'], expected=403, jar=user_jar)
        reply = request('/api/v1/bridge/demo', 'POST', {'payload': {'text': '演示'}})
        assert reply['payload'] == {'text': '演示'} and reply['version'] == 'v1'
        if binary:
            conn = request('/api/v1/dws/connections', 'POST', {'profile': 'fixture-corp:fixture-user', 'corp_id': 'fixture-corp', 'upstream_user_id': 'fixture-user'}, expected=201)
            path = '/api/v1/dws/connections/' + conn['id']
            request(path + '/groups', 'PUT', {'groups': [{'id': 'fixture-group', 'name': 'fixture'}]}, expected=204)
            assert request(path + '/groups')['items'][0]['id'] == 'fixture-group'
            request(path + '/groups', expected=404, jar=user_jar)
            request('/api/v1/dws/connections', 'POST', {'profile': 'fixture-corp:fixture-user', 'corp_id': 'fixture-corp', 'upstream_user_id': 'fixture-user', 'mode': 'school'}, expected=422)
            request(path, 'DELETE', expected=204)
        else:
            request('/api/v1/dws/connections', expected=503)
        request('/api/v1/users/' + user['id'], 'PUT', {'disabled': True})
        request('/api/v1/auth/me', expected=401, jar=user_jar)
        request('/api/v1/users/' + user['id'], 'DELETE', expected=204)
        request('/api/v1/users/' + user['id'], expected=404)
        request('/api/v1/users/' + admin['id'], 'DELETE', expected=409)
        request('/api/v1/auth/logout', 'POST', expected=204)
        request('/api/v1/auth/me', expected=401)
        process.send_signal(signal.SIGTERM)
        process.communicate(timeout=12)
        assert process.returncode == 0
        print('PASS: health, embedded admin page, login, CRUD, RBAC, CSRF, session revocation, last admin, bridge, DWS scope/config, graceful shutdown')
    finally:
        if process.poll() is None:
            process.kill()
            process.communicate(timeout=5)


if __name__ == '__main__':
    main()
