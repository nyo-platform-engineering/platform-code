# The shell captures this token directly into a Kubernetes Secret, never a repo file.
runner = Ci::Runner.find_by(description: 'local-kubernetes')
unless runner
  result = Ci::Runners::CreateRunnerService.new(
    user: User.find_by_username!('root'),
    params: { runner_type: 'instance_type', description: 'local-kubernetes',
              run_untagged: true, tag_list: ['kubernetes'], maximum_timeout: 600 }
  ).execute
  raise result.message unless result.success?
  runner = result.payload.fetch(:runner)
end
puts "RUNNER_TOKEN:#{runner.token}"
