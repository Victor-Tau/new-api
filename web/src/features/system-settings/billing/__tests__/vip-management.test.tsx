/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import i18next from 'i18next'
import { useState } from 'react'
import { expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { BillingSettings } from '../../types'
import { getBillingSectionNavItems } from '../section-registry'
import { VipManagementSection } from '../vip-management-section'

const tiers = Array.from({ length: 6 }, (_, level) => ({
  level,
  key: 'VIP' + level,
  name: 'Tier ' + level,
  min_amount: level * 100,
}))

function Fixture() {
  const [actions, setActions] = useState<HTMLDivElement | null>(null)
  const [queryClient] = useState(() => new QueryClient())
  const settings = {
    VipTierSetting: JSON.stringify({ enabled: true, window_days: 365, tiers }),
    EmailGroupRules: '{"enabled":true,"apply_on_bind":true,"rules":[]}',
    TopupGroupRatio: '{"VIP1":0.99,"学生":0.8}',
    GroupRatio: '{"VIP0":1,"VIP1-大学生优惠":0.8,"学生":0.8}',
  } as BillingSettings

  return (
    <QueryClientProvider client={queryClient}>
      <SettingsPageProvider actionsContainer={actions}>
        <div ref={setActions} />
        <VipManagementSection settings={settings} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

it('links to VIP management and saves tier and email rule changes together', async () => {
  expect(getBillingSectionNavItems(i18next.t)).toEqual(
    expect.arrayContaining([
      expect.objectContaining({ title: 'VIP Management' }),
    ])
  )
  const patch = vi
    .spyOn(api, 'patch')
    .mockResolvedValue({ data: { success: true } })
  try {
    render(<Fixture />)
    const vip1 = screen.getByRole('group', { name: 'VIP1' })
    fireEvent.change(within(vip1).getByLabelText('Top-up Ratio'), {
      target: { value: '0.96' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add Rule' }))
    fireEvent.change(screen.getByLabelText('Domain suffix'), {
      target: { value: 'edu.cn' },
    })
    const discountGroup = screen.getByLabelText('Discount group')
    expect(
      within(discountGroup).getByRole('option', { name: 'VIP0' })
    ).toBeVisible()
    expect(
      within(discountGroup).getByRole('option', { name: '学生' })
    ).toBeVisible()
    fireEvent.change(discountGroup, {
      target: { value: 'VIP1-大学生优惠' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(patch).toHaveBeenCalledOnce())
    expect(patch).toHaveBeenCalledWith('/api/option/vip', {
      vip_tier_setting: { enabled: true, window_days: 365, tiers },
      email_group_rules: {
        enabled: true,
        apply_on_bind: true,
        rules: [{ suffix: 'edu.cn', group: 'VIP1-大学生优惠' }],
      },
      topup_ratios: { VIP0: 1, VIP1: 0.96, VIP2: 1, VIP3: 1, VIP4: 1, VIP5: 1 },
    })
  } finally {
    patch.mockRestore()
  }
})
