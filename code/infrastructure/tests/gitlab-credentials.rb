# Run with Ruby locally or in toolbox:
# ruby tests/gitlab-credentials.rb scripts/gitlab-credentials.rb
# In toolbox, pass the helper's source as the first argument instead of a path.
require 'base64'
require 'json'
require 'open3'
require 'rbconfig'

argument = ARGV.fetch(0)
helper = argument.include?("\n") ? argument : File.read(argument)
encode = ->(value) { Base64.strict_encode64(value) }
repository = { 'data' => { 'username' => encode.call('deploy-user'), 'password' => encode.call('existing-token') } }
registry = { 'data' => { '.dockerconfigjson' => encode.call(JSON.generate(
  auths: { 'registry.localhost' => { auth: encode.call('deploy-user:existing-token') } }
)) } }

run = lambda do |repo, reg|
  output, error, status = Open3.capture3(RbConfig.ruby, '-e', helper,
    stdin_data: JSON.generate(app: 'go-demo', repository: repo, registry: reg))
  [output, error, status]
end

output, error, status = run.call(repository, nil)
raise error unless status.success?
items = JSON.parse(output).fetch('items')
raise 'Changed surviving repository Secret' unless items.length == 1 && items.first.dig('metadata', 'name') == 'go-demo-registry'
config = JSON.parse(Base64.strict_decode64(items.first.fetch('data').fetch('.dockerconfigjson')))
raise 'Rotated registry credentials' unless config.dig('auths', 'registry.localhost', 'auth') == encode.call('deploy-user:existing-token')
puts 'Passed: missing registry Secret reuses repository credentials'

output, error, status = run.call(nil, registry)
raise error unless status.success?
items = JSON.parse(output).fetch('items')
raise 'Changed surviving registry Secret' unless items.length == 1 && items.first.dig('metadata', 'name') == 'go-demo-repository'
raise 'Rotated repository credentials' unless items.first.fetch('data').slice('username', 'password') == repository.fetch('data')
puts 'Passed: missing repository Secret reuses registry credentials'

output, error, status = run.call(repository, registry)
raise error unless status.success?
raise 'Rewrote complete credential pair' unless JSON.parse(output).fetch('items').empty?
puts 'Passed: complete credential pair requires no changes'

_, _, status = run.call({ 'data' => { 'username' => '', 'password' => '' } }, nil)
raise 'Accepted malformed existing credentials' if status.success?
puts 'Passed: malformed credentials fail without replacing them'
