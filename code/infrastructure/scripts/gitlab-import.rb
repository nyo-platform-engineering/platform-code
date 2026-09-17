# Runs inside GitLab toolbox with gitlab-rails runner. Tokens never leave this process.
require 'base64'
require 'json'
require 'net/http'
require 'securerandom'
require 'uri'

root = User.find_by_username!('root')
token = root.personal_access_tokens.build(name: 'local-app-import', scopes: ['api'], expires_at: Date.tomorrow)
token.set_token(SecureRandom.hex(10))
token.save!

begin
  api = lambda do |method, path, body = nil|
    uri = URI("http://gitlab-webservice-default:8181/api/v4#{path}")
    request = method.new(uri)
    request['Host'] = 'gitlab.localhost'
    request['PRIVATE-TOKEN'] = token.token
    request['Content-Type'] = 'application/json'
    request.body = JSON.generate(body) if body
    response = Net::HTTP.start(uri.host, uri.port, open_timeout: 10, read_timeout: 90) { |http| http.request(request) }
    result = JSON.parse(response.body)
    unless response.is_a?(Net::HTTPSuccess) || (method == Net::HTTP::Get && response.code == '404')
      raise "GitLab API #{response.code}: #{result['message']}"
    end

    [response.code, result]
  end

  Dir.children('/workspace/apps').sort.each do |name|
    directory = File.join('/workspace/apps', name)
    next unless File.directory?(directory) && !File.symlink?(directory)
    next unless name.match?(/\A[a-z0-9][a-z0-9_-]*\z/)

    status, project = api.call(Net::HTTP::Get, "/projects/#{URI.encode_www_form_component("root/#{name}")}")
    if status == '404'
      _, project = api.call(Net::HTTP::Post, '/projects', {
        name: name, path: name, namespace_id: root.namespace.id, visibility: 'private', initialize_with_readme: false
      })
    end

    if name == 'go-demo'
      api.call(Net::HTTP::Put, "/projects/#{project.fetch('id')}", { ci_push_repository_for_job_token_allowed: true })
      if ENV['LOCAL_IMAGE_TAG']
        content = "image:\n  repository: local/go-demo\n  tag: #{ENV.fetch('LOCAL_IMAGE_TAG')}\n  digest: ''\nimagePullSecrets:\n  - name: go-demo-registry\n"
        api.call(Net::HTTP::Post, "/projects/#{project.fetch('id')}/repository/commits", {
          branch: 'main', commit_message: 'Use local Go demo image [skip ci]',
          actions: [{ action: 'update', file_path: 'deploy/values.yaml', content: content }]
        })
        puts 'Selected the locally imported Go demo image.'
        next
      end
    end

    unless project['empty_repo']
      changes = []
      ['.gitlab-ci.yml', 'deploy/values.yaml'].each do |relative|
        local_file = File.join(directory, relative)
        next unless File.file?(local_file)
        status, existing = api.call(Net::HTTP::Get, "/projects/#{project.fetch('id')}/repository/files/#{URI.encode_www_form_component(relative)}?ref=main")
        # Preserve the image selected by CI on subsequent imports.
        next if relative == 'deploy/values.yaml' && status != '404'
        content = File.binread(local_file)
        if status == '404' || Base64.decode64(existing.fetch('content')).b != content.b
          changes << { action: status == '404' ? 'create' : 'update', file_path: relative,
                       content: Base64.strict_encode64(content), encoding: 'base64' }
        end
      end
      unless changes.empty?
        api.call(Net::HTTP::Post, "/projects/#{project.fetch('id')}/repository/commits", {
          branch: 'main', commit_message: 'Configure GitLab CI', actions: changes
        })
        puts "Configured CI in root/#{name}."
      end
      puts "Project already contains commits; preserved root/#{name}."
      next
    end

    files = Dir.glob(File.join(directory, '**', '*'), File::FNM_DOTMATCH).sort.select do |file|
      relative = file.delete_prefix("#{directory}/")
      File.file?(file) && !File.symlink?(file) && !relative.split('/').include?('.git') && !file.end_with?('.tar')
    end
    actions = files.map do |file|
      { action: 'create', file_path: file.delete_prefix("#{directory}/"),
        content: Base64.strict_encode64(File.binread(file)), encoding: 'base64', execute_filemode: File.executable?(file) }
    end
    next if actions.empty?

    api.call(Net::HTTP::Post, "/projects/#{project.fetch('id')}/repository/commits", {
      branch: 'main', commit_message: 'Import local app source', actions: actions
    })
    puts "Imported #{actions.length} files into root/#{name}."
  end
ensure
  token.revoke!
end
