local msg = require('mp.msg')
local utils = require('mp.utils')
local options = require('mp.options')

local client_running
local torrents = {}
local TORRENT_PATTERNS = { "%.torrent$", "^magnet:%?xt=urn:btih:", "^http[s]?://", "^" .. string.rep('%x', 40) .. "$" }
local EXCLUDE_PATTERNS = { "127%.0%.0%.1", "192%.168%.%d+%.%d+", "/torrents/" }

local opts = {
  DeleteTorrentFilesOnExit = false,
  DisableUTP = true,
  DownloadDir = os.getenv("tmp"),
  MaxConnsPerTorrent = 200,
  Port = 6969,
  Readahead = 32 * 1024 * 1024,
  ResumeTorrents = true,
  startClientOnMpvLaunch = true,
  closeClientOnMpvExit = true,
  closeClientOnNoTorrentFiles = false -- close torrent client when there are no files from torrents in mpv's playlist
}
options.read_options(opts)

local function load_options()
  local t = {}
  for i, v in pairs(opts) do
    local first_char = i:sub(1, 1)
    if string.upper(first_char) == first_char then
      t[#t + 1] = '--' .. i .. '=' .. tostring(v)
    end
  end
  return t
end

local function start_torrent_client()
  if not client_running then
    local res = mp.command_native({
      name = 'subprocess',
      playback_only = false,
      capture_stdout = false,
      capture_stderr = true, -- change back to true to disable debugging
      args = { mp.get_script_directory() .. "/go_torrent_mpv.exe", table.unpack(load_options()) },
      detach = true
    })

    if res.status < 0 then
      msg.debug('status:', res.status)
      msg.debug('error_string:', res.error_string)
      msg.debug('stdout:', res.stdout)
      msg.debug('stderr:', res.stderr)
      return
    end

    msg.debug('Started torrent server')
    client_running = true
  end
end

local function close_torrent_client()
  if client_running then
    mp.command_native({
      name = 'subprocess',
      playback_only = false,
      capture_stdout = true,
      args = { 'curl', 'localhost:' .. opts.Port .. '/exit' },
      detach = true
    })
    msg.debug('Closed torrent server')
    client_running = false
  end
end

local function add_torrent(torrent_url)
  if not client_running then
    msg.error('Server must be online to add torrents')
    return
  end

  local playlist_req = mp.command_native({
    name = 'subprocess',
    capture_stdout = true,
    args = { 'curl', '-s', '--retry', '10', '--retry-delay', '1', '--retry-connrefused', '-d',
      torrent_url, 'localhost:' .. opts.Port .. '/torrents' }
  })

  local playlist = playlist_req.stdout

  if not playlist or #playlist == 0 then
    msg.debug('Unable to get playlist for', torrent_url)
    return
  end

  return playlist
end

local function remove_torrent(info_hash)
  if not client_running then
    msg.error('Server must be online to remove torrents')
    return
  end

  if not torrents[info_hash] then
    msg.error('Torrent', info_hash, 'does not exist')
    return
  end

  mp.command_native({
    name = 'subprocess',
    playback_only = false,
    args = { 'curl', '-X', 'DELETE', 'localhost:' .. opts.Port .. '/torrents/' .. info_hash },
    detach = true
  })
  torrents[info_hash] = nil
end

local function index_playlist(playlist)
  local info_hash = playlist:match(string.rep('%x', 40))
  torrents[info_hash] = {}

  for line in playlist:gmatch('([^%\n]+)') do
    if not line:find('^#') then
      table.insert(torrents[info_hash], line)
    end
  end
end

local function playlist_changed(_, playlist)
  for info_hash, files in pairs(torrents) do
    local has_file = false
    for _, file in pairs(files) do
      for _, playlist_entry in pairs(playlist) do
        if playlist_entry.filename == file then
          has_file = true
          break
        end
      end
      if has_file then
        break
      end
    end

    if not has_file then
      remove_torrent(info_hash)
    end
  end
end

local function on_load(hook)
  local path = mp.get_property('stream-open-filename', '')

  for _, pattern in ipairs(EXCLUDE_PATTERNS) do
    if path:find(pattern) then
      return
    end
  end

  msg.debug('Loading', path)

  for _, pattern in ipairs(TORRENT_PATTERNS) do
    if path:find(pattern) then
      msg.debug('Pattern', pattern, 'found')
      start_torrent_client()
      local playlist = add_torrent(path)

      if playlist then
        index_playlist(playlist)
        mp.set_property('stream-open-filename', 'memory://' .. playlist)
        return
      else
        break
      end
    end
  end
  msg.debug('Unable to load', path)

  if next(torrents) == nil and opts.closeClientOnNoTorrentFiles then
    close_torrent_client()
  end
end

mp.add_hook('on_load', 50, on_load)
mp.observe_property('playlist', 'native', playlist_changed)
if opts.closeClientOnMpvExit then
  mp.register_event('shutdown', close_torrent_client)
end
if opts.startClientOnMpvLaunch then
  start_torrent_client()
end
