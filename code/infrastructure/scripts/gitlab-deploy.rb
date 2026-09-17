# Runs in toolbox. Output is captured and piped directly into kubectl, never logged.
require 'base64'
require 'json'
require 'net/http'
require 'securerandom'

root = User.find_by_username!('root')
token = root.personal_access_tokens.build(name: 'local-deploy-setup', scopes: ['api'], expires_at: Date.tomorrow)
token.set_token(SecureRandom.hex(10))
token.save!
begin
  project = Project.find_by_full_path('root/go-demo') or raise 'Import root/go-demo first.'
  uri = URI("http://gitlab-webservice-default:8181/api/v4/projects/#{project.id}/deploy_tokens")
  request = Net::HTTP::Post.new(uri)
  request['Host'] = 'gitlab.localhost'
  request['PRIVATE-TOKEN'] = token.token
  request['Content-Type'] = 'application/json'
  request.body = JSON.generate(name: 'local-argocd', scopes: %w[read_repository read_registry])
  response = Net::HTTP.start(uri.host, uri.port, open_timeout: 10, read_timeout: 90) { |http| http.request(request) }
  raise "Deploy credential setup failed: #{response.code}" unless response.is_a?(Net::HTTPSuccess)
  credential = JSON.parse(response.body)
  auth = Base64.strict_encode64("#{credential.fetch('username')}:#{credential.fetch('token')}")
  repo = { apiVersion: 'v1', kind: 'Secret', type: 'Opaque',
    metadata: { name: 'go-demo-repository', namespace: 'argocd', labels: { 'argocd.argoproj.io/secret-type' => 'repository' } },
    stringData: { type: 'git', url: 'http://gitlab-webservice-default.gitlab.svc.cluster.local:8181/root/go-demo.git',
                  username: credential.fetch('username'), password: credential.fetch('token') } }
  registry = { apiVersion: 'v1', kind: 'Secret', type: 'kubernetes.io/dockerconfigjson',
    metadata: { name: 'go-demo-registry', namespace: 'dev' },
    stringData: { '.dockerconfigjson' => JSON.generate(auths: { 'registry.localhost' => { auth: auth } }) } }
  puts JSON.generate(apiVersion: 'v1', kind: 'List', items: [repo, registry])
ensure
  token.revoke!
end
