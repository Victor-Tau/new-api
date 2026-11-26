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
import { render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { PaymentConfirmDialog } from '../payment-confirm-dialog'

describe('wallet payment confirmation', () => {
  it('shows the separately charged fee and the total in the checkout currency', () => {
    render(
      <PaymentConfirmDialog
        open
        onOpenChange={vi.fn()}
        onConfirm={vi.fn()}
        topupAmount={10}
        paymentAmount={13.8}
        serviceFee={{ amount: 2.35, currency: 'HKD' }}
        paymentMethod={{ name: 'Stripe', type: 'stripe' }}
        calculating={false}
        processing={false}
      />
    )

    const dialog = within(screen.getByRole('alertdialog'))
    expect(dialog.getByText('Small top-up service fee')).toBeVisible()
    expect(dialog.getByText('2.35 HKD')).toBeVisible()
    expect(dialog.getByText('13.8 HKD')).toBeVisible()
  })
})
