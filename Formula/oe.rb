class Oe < Formula
  desc "Command-line tools for OpenE2EE projects"
  homepage "https://github.com/open-e2ee/oe"
  url "https://github.com/open-e2ee/oe/archive/refs/tags/v1.0.0.tar.gz"
  sha256 "0cf10c4cd8974dbd82fb6c477970c5fdd982232432c2e8a43c043ac618235cb9"
  license "Apache-2.0"
  head "https://github.com/open-e2ee/oe.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-X main.version=#{version}"), "./cmd/oe"
  end

  test do
    assert_match "\"command\":\"version\"", shell_output("#{bin}/oe --json version")
  end
end
