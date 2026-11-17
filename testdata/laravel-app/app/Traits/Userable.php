<?php

namespace App\Traits;

use App\Models\User;

trait Userable
{
    protected User $user {
        get => auth()->user();
    }
}
