import { describe, expect, it } from 'vitest'
import { nodeIdHint, nodeInstallCommand, replaceLoopbackHost } from './nodeEnrollmentModel'

describe('nodeIdHint', () => {
  it('accepts the empty string, since the field is simply unfilled', () => {
    expect(nodeIdHint('')).toBeNull()
  })

  it('accepts a plain lowercase id', () => {
    expect(nodeIdHint('render-01')).toBeNull()
  })

  it('refuses an uppercase character', () => {
    expect(nodeIdHint('Render-01')).not.toBeNull()
  })

  it('refuses a leading hyphen', () => {
    expect(nodeIdHint('-render')).not.toBeNull()
  })

  it('refuses a trailing hyphen', () => {
    expect(nodeIdHint('render-')).not.toBeNull()
  })

  it('refuses an id longer than 64 characters', () => {
    expect(nodeIdHint('a'.repeat(65))).not.toBeNull()
  })

  it('accepts an id exactly 64 characters', () => {
    expect(nodeIdHint('a'.repeat(64))).toBeNull()
  })

  it.each(['enroll', 'enrollments', 'coordinator', 'fpp', 'healthcheck', 'observer'])('refuses the reserved id %s', (id) => {
    expect(nodeIdHint(id)).not.toBeNull()
  })
})

describe('nodeInstallCommand', () => {
  it('builds the curl-and-run form for a tagged release version', () => {
    const command = nodeInstallCommand('1.2.3', 'https://coordinator.example', 'ABCD-2345')
    expect(command).toBe(
      'curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v1.2.3/get-showmesh.sh | sudo bash -s -- --coordinator https://coordinator.example --code ABCD-2345',
    )
  })

  it('builds the installer form for a non-release version string', () => {
    const command = nodeInstallCommand('dev', 'https://coordinator.example', 'ABCD-2345')
    expect(command).toBe('sudo showmesh-install --coordinator https://coordinator.example --code ABCD-2345')
  })

  it('builds the installer form when the version is unknown', () => {
    const command = nodeInstallCommand('', 'https://coordinator.example', 'ABCD-2345')
    expect(command).toBe('sudo showmesh-install --coordinator https://coordinator.example --code ABCD-2345')
  })
})

describe('replaceLoopbackHost', () => {
  it('replaces localhost with the placeholder', () => {
    const { url, loopback } = replaceLoopbackHost('http://localhost:8080')
    expect(loopback).toBe(true)
    expect(url).toBe('http://COORDINATOR-ADDRESS:8080')
  })

  it('replaces 127.0.0.1 with the placeholder', () => {
    const { url, loopback } = replaceLoopbackHost('http://127.0.0.1:8080')
    expect(loopback).toBe(true)
    expect(url).toBe('http://COORDINATOR-ADDRESS:8080')
  })

  it('replaces the bracketed IPv6 loopback with the placeholder', () => {
    const { url, loopback } = replaceLoopbackHost('http://[::1]:8080')
    expect(loopback).toBe(true)
    expect(url).toBe('http://COORDINATOR-ADDRESS:8080')
  })

  it('replaces any 127.0.0.0/8 address with the placeholder', () => {
    const { url, loopback } = replaceLoopbackHost('http://127.0.0.2:8080')
    expect(loopback).toBe(true)
    expect(url).toBe('http://COORDINATOR-ADDRESS:8080')
  })

  it('leaves a real network address unchanged', () => {
    const { url, loopback } = replaceLoopbackHost('https://showmesh.example.com')
    expect(loopback).toBe(false)
    expect(url).toBe('https://showmesh.example.com')
  })

  it('leaves an unparseable value unchanged', () => {
    const { url, loopback } = replaceLoopbackHost('')
    expect(loopback).toBe(false)
    expect(url).toBe('')
  })
})
