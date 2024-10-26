-- Utils
local msg = require("mp.msg")
local utils = require("mp.utils")
local options = require("mp.options")

-- Constants
local TORRENT_PATTERNS = { "%.torrent$", "^magnet:%?xt=urn:btih:", "^http[s]?://", "^" .. string.rep("%x", 40) .. "$" }
local EXCLUDE_PATTERNS = { "127%.0%.0%.1", "192%.168%.%d+%.%d+", "/torrents/" }

-- State management
local State = {
  client_running = false,
  launched_by_us = false,
  torrents = {},
}

-- Configuration
local Config = {
  opts = {
    DeleteDatabaseOnExit = false,
    DeleteDataOnTorrentDrop = false,
    DisableUTP = true,
    DownloadDir = os.getenv("tmp"),
    MaxConnsPerTorrent = 200,
    Port = 6969,
    Readahead = 32 * 1024 * 1024,
    Responsive = false,
    ResumeTorrents = true,
    Profiling = false,
    startClientOnMpvLaunch = true,
    closeClientOnMpvExit = true,
    closeClientOnNoTorrentFiles = false
  },

  load = function(self)
    options.read_options(self.opts)
    return self
  end,

  get_client_args = function(self)
    local args = {}
    for i, v in pairs(self.opts) do
      local first_char = i:sub(1, 1)
      if string.upper(first_char) == first_char then
        args[#args + 1] = "--" .. i .. "=" .. tostring(v)
      end
    end
    return args
  end
}

-- Client management
local Client = {
  is_running = function(self)
    local cmd = mp.command_native({
      name = "subprocess",
      playback_only = false,
      capture_stdout = true,
      capture_stderr = true,
      args = { "curl", "-s", "--connect-timeout", "0.25", "localhost:" .. Config.opts.Port .. "/torrents" }
    })
    return cmd.status == 0
  end,

  update_status = function(self)
    if State.client_running then
      local cmd = mp.command_native({
        name = "subprocess",
        playback_only = false,
        capture_stdout = true,
        capture_stderr = true,
        args = { "curl", "-s", "--connect-timeout", "5", "localhost:" .. Config.opts.Port .. "/torrents" }
      })

      if cmd.status ~= 0 then
        return nil
      end

      local t = utils.parse_json(cmd.stdout)
      for _, v in pairs(t) do
        State.torrents[v.InfoHash] = { Name = v.Name, Files = v.Files, Length = v.Length }
      end
    end
  end,

  start = function(self)
    if not State.client_running then
      if self:is_running() then
        msg.debug("Client is already running")
        State.client_running = true
        return true
      end

      local res = mp.command_native({
        name = "subprocess",
        playback_only = false,
        capture_stderr = true,
        args = { mp.get_script_directory() .. "/go_torrent_mpv.exe", table.unpack(Config:get_client_args()) },
        detach = true
      })

      if res.status ~= 0 then
        msg.debug("Failed to start client:", res.stderr)
        return false
      end

      msg.debug("Started torrent server")
      State.client_running = true
      State.launched_by_us = true
      return true
    end
    return false
  end,

  close = function(self)
    if State.client_running and State.launched_by_us then
      mp.command_native({
        name = "subprocess",
        playback_only = false,
        capture_stderr = true,
        args = { "curl", "localhost:" .. Config.opts.Port .. "/exit" }
      })
      msg.debug("Closed torrent server")
      State.client_running = false
      State.launched_by_us = false
    end
  end
}

-- Torrent operations
local TorrentOps = {
  add = function(self, torrent_url)
    if not State.client_running then
      msg.error("Server must be online to add torrents")
      return nil
    end

    local playlist_req = mp.command_native({
      name = "subprocess",
      capture_stdout = true,
      args = { "curl", "-s", "--retry", "10", "--retry-delay", "1", "--retry-connrefused", "-d",
        torrent_url, "localhost:" .. Config.opts.Port .. "/torrents" }
    })

    local playlist = playlist_req.stdout
    if not playlist or #playlist == 0 then
      msg.debug("Unable to get playlist for", torrent_url)
      return nil
    end

    return playlist
  end,

  remove = function(self, info_hash)
    if not State.client_running then
      msg.error("Server must be online to remove torrents")
      return false
    end

    if not State.torrents[info_hash] then
      msg.error("Torrent", info_hash, "does not exist")
      return false
    end

    mp.command_native({
      name = "subprocess",
      playback_only = false,
      args = { "curl", "-X", "DELETE", "localhost:" .. Config.opts.Port .. "/torrents/" .. info_hash },
      detach = true
    })
    State.torrents[info_hash] = nil
    return true
  end
}

-- Menu integration
local Menu = {
  create_torrent_menu = function(self)
    local menu_items = {}
    Client:update_status()

    -- Add client control items
    table.insert(menu_items, {
      title = "Client Controls",
      items = {
        {
          title = State.client_running and "Stop Client" or "Start Client",
          icon = State.client_running and "stop" or "play_arrow",
          value = State.client_running and "script-message-to go_torrent_mpv client-stop" or
              "script-message-to go_torrent_mpv client-start"
        }
      }
    })

    if State.client_running then

      local remove_torrents_submenu = {}
      for i, v in pairs(State.torrents) do
        local submenu_items = {}
        table.insert(remove_torrents_submenu, {
          title = v.Name,
          hint = string.format("%.1f GB", v.Length / (1024 * 1024 * 1024)),
          value = i,
          actions = {
            {name = "delete", icon = "delete", label = "Delete torrent"},
            {name = "delete_files", icon = "delete_forever", label = "Delete torrent & files"}
          }
        })

      end
      table.insert(menu_items, {
        title = "Remove Torrent",
        items = remove_torrents_submenu
      })


      -- Add items for each torrent
      for i, v in pairs(State.torrents) do
        local submenu_items = {}
        for _, file in pairs(v.Files) do
          table.insert(submenu_items, {
            title = file.Name,
            hint = string.format("%.1f MB", file.Length / (1024 * 1024)),
            value = string.format("loadfile \"%s\"", file.URL),
          })
        end

        table.insert(menu_items, {
          title = v.Name,
          hint = string.format("%d files", #v.Files),
          items = submenu_items
        })
      end
    end

    return {
      type = "torrent_menu",
      title = "Torrent Manager",
      items = menu_items,
      callback = { mp.get_script_name(), "menu-callback" }
    }
  end,

  show = function(self)
    local menu_data = self:create_torrent_menu()
    mp.commandv("script-message-to", "uosc", "open-menu", utils.format_json(menu_data))
  end,

  handle_callback = function(self, json_event)
    local event = utils.parse_json(json_event)
    if event.type == "activate" then
      if event.action == "delete" then
        TorrentOps:remove(event.value)
        self:show()
      elseif event.action == "delete_files" then
        TorrentOps:remove(event.value)
        self:show()
      else
        if event.menu_id ~= "Remove Torrent" then
          mp.command(event.value)
        end
      end
      if event.menu_id ~= "Client Controls" and event.menu_id ~= "Remove Torrent" then
        mp.commandv("script-message-to", "uosc", "close-menu", "torrent_menu")
      end
    end
  end
}

-- Event handlers
local function on_file_loaded()
  local path = mp.get_property("stream-open-filename", "")

  for _, pattern in ipairs(EXCLUDE_PATTERNS) do
    if path:find(pattern) then return end
  end

  for _, pattern in ipairs(TORRENT_PATTERNS) do
    if path:find(pattern) then
      if Client:start() then
        local playlist = TorrentOps:add(path)
        if playlist then
          Client:update_status()
          mp.set_property("stream-open-filename", "memory://" .. playlist)
          return
        end
      end
      break
    end
  end

  if next(State.torrents) == nil and Config.opts.closeClientOnNoTorrentFiles then
    Client:close()
  end
end

-- Script initialization
local function init()
  Config:load()

  -- Register menu command
  mp.add_key_binding("Alt+t", "toggle-torrent-menu", function()
    Menu:show()
  end)

  -- Register menu callback handler
  mp.register_script_message("menu-callback", function(json)
    Menu:handle_callback(json)
  end)

  -- Register client control handlers
  mp.register_script_message("client-start", function()
    Client:start()
    Menu:show()
  end)

  mp.register_script_message("client-stop", function()
    Client:close()
    Menu:show()
  end)

  -- Register MPV event handlers
  mp.add_hook("on_load", 50, on_file_loaded)

  if Config.opts.closeClientOnMpvExit then
    mp.register_event("shutdown", function() Client:close() end)
  end

  if Config.opts.startClientOnMpvLaunch then
    Client:start()
  end
end

init()
