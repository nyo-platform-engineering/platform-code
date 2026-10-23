# Runs with toolbox Ruby; Secret JSON enters on stdin and never reaches logs.
require 'base64'
require 'json'

input = JSON.parse($stdin.read)
app = input.fetch('app')
raise 'Unsupported application' unless %w[go-demo telemetry-ui].include?(app)

if (repository = input['repository'])
  username = Base64.strict_decode64(repository.fetch('data').fetch('username'))
  password = Base64.strict_decode64(repository.fetch('data').fetch('password'))
else
  config = JSON.parse(Base64.strict_decode64(input.fetch('registry').fetch('data').fetch('.dockerconfigjson')))
  entry = config.fetch('auths').fetch('registry.localhost')
  username, password = Base64.strict_decode64(entry.fetch('auth')).split(':', 2)
end
raise 'Existing deploy credentials are empty or malformed' if username.to_s.empty? || password.to_s.empty?

items = []
unless input['repository']
  items << { apiVersion: 'v1', kind: 'Secret', type: 'Opaque',
    metadata: { name: "#{app}-repository", namespace: 'argocd', labels: { 'argocd.argoproj.io/secret-type' => 'repository' } },
    data: { type: Base64.strict_encode64('git'),
            url: Base64.strict_encode64("http://gitlab-webservice-default.gitlab.svc.cluster.local:8181/root/#{app}.git"),
            username: Base64.strict_encode64(username), password: Base64.strict_encode64(password) } }
end
unless input['registry']
  config = JSON.generate(auths: { 'registry.localhost' => { auth: Base64.strict_encode64("#{username}:#{password}") } })
  items << { apiVersion: 'v1', kind: 'Secret', type: 'kubernetes.io/dockerconfigjson',
    metadata: { name: "#{app}-registry", namespace: 'dev' },
    data: { '.dockerconfigjson' => Base64.strict_encode64(config) } }
end
puts JSON.generate(apiVersion: 'v1', kind: 'List', items: items)
