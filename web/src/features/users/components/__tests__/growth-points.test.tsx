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
  renderHook,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'

import * as api from '../../api'
import type { User } from '../../types'
import { useUsersColumns } from '../users-columns'
import { UsersMutateDrawer } from '../users-mutate-drawer'
import { UsersProvider } from '../users-provider'

const user: User = {
  id: 7,
  username: 'new-member',
  display_name: 'New member',
  quota: 500000,
  used_quota: 0,
  request_count: 0,
  group: 'VIP0',
  growth_amount: 250.5,
  role: 1,
  status: 1,
}

function renderDrawer() {
  const getUser = vi
    .spyOn(api, 'getUser')
    .mockResolvedValue({ success: true, data: user })
  vi.spyOn(api, 'getGroups').mockResolvedValue({
    success: true,
    data: ['VIP0', 'VIP1'],
  })
  vi.spyOn(api, 'getPermissionCatalog').mockResolvedValue({
    resources: [],
    roles: [],
  })
  const updateUser = vi.spyOn(api, 'updateUser')
  const onOpenChange = vi.fn()
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  const drawer = (row: User) => (
    <QueryClientProvider client={queryClient}>
      <UsersProvider>
        <UsersMutateDrawer open currentRow={row} onOpenChange={onOpenChange} />
      </UsersProvider>
    </QueryClientProvider>
  )
  const rendered = render(drawer(user))
  return {
    getUser,
    updateUser,
    onOpenChange,
    switchUser: (row: User) => rendered.rerender(drawer(row)),
  }
}

async function openGrowthDialog() {
  const trigger = await screen.findByRole('button', {
    name: 'Adjust growth points',
  })
  await waitFor(() => expect(trigger).toBeEnabled())
  fireEvent.click(trigger)
  return screen.findByRole('dialog', { name: 'Adjust growth points' })
}

describe('user growth points', () => {
  test('places growth points immediately after invitation information', () => {
    const { result } = renderHook(useUsersColumns)
    const invite = result.current.findIndex(
      (column) => column.id === 'invite_info'
    )
    expect(invite).toBeGreaterThanOrEqual(0)
    expect(result.current[invite + 1]?.header).toBe('Growth points')
  })

  test('saves growth separately and preserves unsaved user edits', async () => {
    const adjust = vi.spyOn(api, 'adjustUserGrowth').mockResolvedValue({
      growth_amount: 600.25,
      group: 'VIP1',
    })
    const { updateUser, onOpenChange } = renderDrawer()
    const remark = await screen.findByRole('textbox', { name: 'Remark' })
    await screen.findByDisplayValue('250.5')
    fireEvent.change(remark, { target: { value: 'Unsaved remark' } })
    const dialog = await openGrowthDialog()
    const input = within(dialog).getByRole('spinbutton', {
      name: 'Growth points',
    })
    expect(input).toHaveValue(250.5)
    fireEvent.change(input, { target: { value: '600.25' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Adjust growth points' })
      ).not.toBeInTheDocument()
    )
    expect(screen.getByRole('textbox', { name: 'Growth points' })).toHaveValue(
      '600.25'
    )
    expect(screen.getByRole('combobox', { name: 'Group' })).toHaveValue('VIP1')
    expect(adjust).toHaveBeenCalledWith(user.id, 600.25)
    expect(remark).toHaveValue('Unsaved remark')
    expect(updateUser).not.toHaveBeenCalled()
    expect(onOpenChange).not.toHaveBeenCalled()
  })

  test('waits for the newly selected user before allowing growth adjustments', async () => {
    const { getUser, switchUser } = renderDrawer()
    await screen.findByDisplayValue('250.5')
    getUser.mockReturnValueOnce(new Promise(() => undefined))

    switchUser({ ...user, id: 8, username: 'another-member' })

    expect(
      screen.getByRole('button', { name: 'Adjust growth points' })
    ).toBeDisabled()
    expect(screen.queryByDisplayValue('250.5')).not.toBeInTheDocument()
  })

  test('blocks an invalid value and retains a valid value after a server rejection', async () => {
    const adjust = vi
      .spyOn(api, 'adjustUserGrowth')
      .mockRejectedValue(new Error('Growth adjustment failed'))
    renderDrawer()
    const dialog = await openGrowthDialog()
    const input = within(dialog).getByRole('spinbutton', {
      name: 'Growth points',
    })
    const save = within(dialog).getByRole('button', { name: 'Save' })
    fireEvent.change(input, { target: { value: '-1' } })
    fireEvent.click(save)
    expect(
      await within(dialog).findByText('Invalid growth points')
    ).toBeVisible()
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(adjust).not.toHaveBeenCalled()

    fireEvent.change(input, { target: { value: '0' } })
    fireEvent.click(save)
    await waitFor(() => expect(adjust).toHaveBeenCalledWith(user.id, 0))
    await waitFor(() => expect(save).toBeEnabled())
    expect(input).toHaveValue(0)
    expect(dialog).toBeVisible()
  })
})
