import { describe, expect, it } from 'vitest'
import { cameraDraftError, cameraRequest, gatewayRequest, gatewayUrlError } from './previewSettingsModel'

const draft = { id: 'driveway', name: 'Driveway', enabled: true, url: 'rtsp://10.0.0.5:554/live' }

describe('preview settings model', () => {
  it('accepts an empty or http gateway address and refuses anything else', () => {
    expect(gatewayUrlError('')).toBeNull()
    expect(gatewayUrlError('http://10.0.0.5:8095')).toBeNull()
    expect(gatewayUrlError('10.0.0.5:8095')).toMatch(/http:\/\//)
  })

  it('leaves the token out of the gateway write unless one was typed', () => {
    expect(gatewayRequest(' http://x:1 ', '')).toEqual({ url: 'http://x:1' })
    expect(gatewayRequest('http://x:1', 's3cret')).toEqual({ url: 'http://x:1', token: 's3cret' })
  })

  it('requires an id, a name and an rtsp address for a new camera', () => {
    expect(cameraDraftError(draft, 'new', [])).toBeNull()
    expect(cameraDraftError({ ...draft, id: '' }, 'new', [])).toMatch(/id/)
    expect(cameraDraftError({ ...draft, id: 'a/b' }, 'new', [])).toMatch(/slashes/)
    expect(cameraDraftError(draft, 'new', ['driveway'])).toMatch(/already exists/)
    expect(cameraDraftError({ ...draft, name: ' ' }, 'new', [])).toMatch(/name/)
    expect(cameraDraftError({ ...draft, url: '' }, 'new', [])).toMatch(/address/)
    expect(cameraDraftError({ ...draft, url: 'http://x' }, 'new', [])).toMatch(/rtsp:\/\//)
  })

  it('lets an edit keep the stored address by leaving it blank', () => {
    expect(cameraDraftError({ ...draft, url: '' }, 'edit', [])).toBeNull()
    expect(cameraDraftError({ ...draft, url: 'rtsps://h:322/x' }, 'edit', [])).toBeNull()
    expect(cameraDraftError({ ...draft, url: 'ftp://h' }, 'edit', [])).toMatch(/rtsp/)
  })

  it('refuses an address with a space, a tab, a # or no host', () => {
    for (const url of ['rtsp://h/a b', 'rtsp://h/a\tb', 'rtsp://h/a#x', 'rtsp:///x']) {
      expect(cameraDraftError({ ...draft, url }, 'new', [])).toMatch(/must start with rtsp/)
    }
  })

  it('leaves the address out of a camera write unless one was typed', () => {
    expect(cameraRequest({ ...draft, url: '' })).toEqual({ name: 'Driveway', enabled: true })
    expect(cameraRequest(draft)).toEqual({ name: 'Driveway', enabled: true, url: 'rtsp://10.0.0.5:554/live' })
  })
})
