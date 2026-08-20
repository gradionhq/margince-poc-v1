import type { Meta, StoryObj } from "@storybook/react-vite";
import { ReleaseSkewScreen } from "./releaseskew";
import { StoryProviders } from "./story-utils";

/**
 * The fifth outcome of the unauthenticated surface, beside sign-in, password
 * reset, connection problem and installation-unavailable: the bundle in the
 * browser and the api behind it come from different releases, so the app does
 * not render at all.
 *
 * It takes the same two-region frame those four take, with the Core in
 * `unavailable` — the state that does not breathe. A reader must be able to tell
 * this apart from an outage at a glance, because the action is different: an
 * outage asks them to wait, this asks them to reload and, failing that, to tell
 * an operator.
 */
const meta: Meta<typeof ReleaseSkewScreen> = {
  title: "Signed out/Release skew",
  component: ReleaseSkewScreen,
};
export default meta;
type Story = StoryObj<typeof ReleaseSkewScreen>;

/** A torn tag pull: the two images came from different releases and stay that
 *  way until somebody re-pulls the set. */
export const TornSet: Story = {
  render: () => (
    <StoryProviders>
      <ReleaseSkewScreen app="1970.41" server="1970.42" />
    </StoryProviders>
  ),
};

/** The far more common shape of the same defect: a tab held open across a
 *  deploy. Here Reload genuinely is the fix, which is why it is offered. */
export const StaleTabAfterADeploy: Story = {
  render: () => (
    <StoryProviders>
      <ReleaseSkewScreen app="2026.3" server="2026.4" />
    </StoryProviders>
  ),
};
