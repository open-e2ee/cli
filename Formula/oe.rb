class Oe < Formula
  desc "Command-line tools for OpenE2EE projects"
  homepage "https://github.com/open-e2ee/cli"
  url "https://github.com/open-e2ee/cli/archive/refs/tags/v1.0.0.tar.gz"
  sha256 "5200d8b4b2bddcc1592d51974d5e3dc0cd0c3e5d9bb536ca5be6cc61ee3dfd05"
  license "Apache-2.0"
  head "https://github.com/open-e2ee/cli.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.version=#{version}"), "./cmd/oe"
  end

  test do
    assert_match "\"command\":\"version\"", shell_output("#{bin}/oe --json version")
  end
end
